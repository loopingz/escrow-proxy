# Prometheus Metrics on a Separate HTTP Server

**Date:** 2026-05-12
**Status:** Approved, ready for implementation plan

## Problem

The proxy has no observability surface beyond structured logs. Operators
running it in CI or as a long-lived service have no way to:

- Alert when upstream errors spike.
- Track cache hit rate as a SLO.
- Catch CA / TLS cert expiry before it breaks every TLS handshake (the
  motivating concern for the current `fix/certs-expiration` work).
- See request latency or throughput.

We need a Prometheus-compatible metrics endpoint exposed on a
**separate HTTP server** so the proxy listener stays focused on MITM
traffic and can't accidentally expose `/metrics` on the proxy port.

## Goals

- Expose Prometheus metrics at `/metrics` on a dedicated HTTP server.
- Cover four metric groups: HTTP traffic + cache, upstream errors, TLS
  cert expiry, index + Go runtime/process metrics.
- Available in all three modes: `serve`, `record`, `offline`. A `mode`
  label distinguishes traffic.
- Configurable via CLI flag, YAML, and env var. Default `:9090`. Empty
  value disables the metrics server entirely.
- Graceful shutdown alongside the proxy on SIGINT/SIGTERM.

## Non-Goals

- TLS or authentication on `/metrics`. Deployments are expected to put
  the metrics port on an internal network or behind an ingress that
  handles auth.
- Per-leaf-cert expiry gauges. Leaf certs are minted from the CA on
  demand and churn with the cert cache; only the CA expiry matters for
  alerting. May revisit later.
- Tracing or OpenTelemetry export. OTel is in `go.mod` only as a
  transitive dep of the GCS SDK; adding the OTel SDK directly is out of
  scope.
- Exemplars, custom histogram-bucket tuning beyond sensible defaults.
- Tier-index labels (`tier_0`, `tier_1`, …). Tier label is the storage
  *type* (`local|gcs|s3`).

## Library choice

`github.com/prometheus/client_golang`. Industry standard, small surface,
straightforward integration. Uses a **custom `*prometheus.Registry`**
rather than the default global registry, so tests can construct
isolated registries and we never leak collectors across tests.

## Architecture

### New package `internal/metrics`

Owns all collectors, the registry, the HTTP handler, and the dedicated
HTTP server.

- `New() *Metrics` — constructs collectors, returns a struct holding
  the registry and typed accessors for each collector.
- `(m *Metrics) Handler() http.Handler` — returns
  `promhttp.HandlerFor(m.reg, ...)` mounted at `/metrics`.
- `(m *Metrics) StartServer(ctx context.Context, addr string, logger *slog.Logger) (shutdown func(context.Context) error, err error)`
  — starts the secondary `http.Server` in a goroutine. Returns a
  shutdown closure callers invoke during teardown. If `addr == ""` the
  server is not started; `StartServer` returns a no-op shutdown.

Server mounts:

- `GET /metrics` — Prometheus scrape endpoint.
- `GET /healthz` — returns `200 OK`, body `"ok"`. Cheap liveness probe.

### Wiring in `cmd/escrow-proxy/main.go`

`startProxy` gains a `metrics *metrics.Metrics` parameter (or a small
options struct). After binding the proxy listener and before
`ListenAndServe`, it calls `metrics.StartServer(...)` if configured.
The SIGINT/SIGTERM goroutine shuts down both servers (metrics first,
then proxy) inside the existing 5s timeout.

If `cfg.Metrics.Listen == ""`, no metrics server is started and no
collectors are registered into the live wiring — instrumentation calls
become no-ops via a nil-safe pattern (see "Nil-safety" below).

### Instrumentation points

Each surface gets a thin shim that lives near the existing code, so
the metrics package never has to import proxy/cache/tls.

1. **HTTP request counters + duration** —
   `internal/proxy/handler.go` (or the entry point that wraps the
   goproxy `Server`). Wrap the handler in a middleware that records:
   - `escrow_proxy_requests_total{mode,method,status,cache}` (counter)
   - `escrow_proxy_request_duration_seconds{mode,method}` (histogram,
     default buckets)
   Labels:
   - `mode`: `serve|record|offline`
   - `method`: `GET|HEAD|...` (already restricted by config)
   - `status`: numeric HTTP status as string
   - `cache`: `hit|miss|bypass|recorded`

2. **Cache hit/miss/bytes** —
   `internal/cache/cache.go` `Get` path:
   - `escrow_proxy_cache_hits_total{tier}` (counter, `tier=local|gcs|s3`)
   - `escrow_proxy_cache_misses_total` (counter)
   - `escrow_proxy_cache_bytes_served_total` (counter, bytes summed
     across hits)
   For tiered storage, the tier surfaces which underlying storage
   answered. Tiered storage already iterates tiers in order; we extend
   the result to include the type name of the tier that produced the
   bytes. Single-tier configs report that tier's type directly.

3. **Upstream errors** — `internal/proxy/transport.go`. After the
   round-trip returns an error or a 5xx, classify into a `kind` label:
   - `timeout` — `errors.Is(err, context.DeadlineExceeded)` or
     `net.Error.Timeout()`
   - `dial` — `*net.OpError` with `Op == "dial"`
   - `tls` — `*tls.RecordHeaderError`, `tls.CertificateVerificationError`,
     or `x509.*` errors
   - `upstream_5xx` — response with status ≥ 500
   - `other` — fallback
   Counter: `escrow_proxy_upstream_errors_total{kind}`.

4. **TLS cert expiry** — A `prometheus.Collector` implementation in
   `internal/metrics` that holds a reference to the CA and on `Collect`
   computes `time.Until(ca.Cert.NotAfter).Seconds()` and emits a single
   gauge `escrow_proxy_ca_expiry_seconds`. Scrape-time calculation
   keeps the value always fresh without a goroutine.

5. **Index + reindex** — A scrape-time collector reading from the
   `*index.Index` reference. Emits:
   - `escrow_proxy_index_entries` (gauge) — calls `idx.Count(ctx)` with
     a short timeout (1s). On error, logs a warn and skips this gauge.
   - `escrow_proxy_reindex_in_progress` (gauge, 0/1) — set by a flag in
     `cmd/escrow-proxy/main.go`'s auto-reindex goroutine.

6. **Go runtime + process** —
   `collectors.NewGoCollector()` and
   `collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})`
   registered into the same registry. Standard `go_*` / `process_*`
   metrics for free.

### Nil-safety

`*metrics.Metrics` methods (`RecordRequest`, `RecordCacheHit`, etc.)
are safe to call on a nil receiver — they short-circuit to no-ops.
This lets the wiring in `cmd/escrow-proxy/main.go` pass either a real
`*Metrics` or `nil` (when `--metrics-listen ""`) without sprinkling
nil checks at every call site.

## Config

Add to `internal/config/config.go`:

```go
type Config struct {
    // ...existing fields...
    Metrics MetricsConfig `yaml:"metrics"`
}

type MetricsConfig struct {
    Listen string `yaml:"listen"`
}
```

`DefaultConfig()` sets `Metrics: MetricsConfig{Listen: ":9090"}`.

CLI flag (in `cmd/escrow-proxy/main.go`):
```
--metrics-listen string   metrics HTTP server bind address (default ":9090"; empty disables); falls back to $ESCROW_PROXY_METRICS_LISTEN
```

Resolution order (matches existing `--listen` / `--log-level`):
1. CLI flag if `Changed()`.
2. Else `$ESCROW_PROXY_METRICS_LISTEN` if set (handled in `loadConfig`
   the same way `ESCROW_PROXY_LOG_LEVEL` is).
3. Else YAML value.
4. Else default `:9090`.

Empty string at any layer means "disabled".

## Error handling

- **Metrics bind failure**: log `error`, exit 1. A misconfigured port
  is the kind of thing operators want to see loudly, and the proxy is
  presumed running under a supervisor that will restart with a fix.
- **Scrape-time collector errors** (e.g., `idx.Count()` returns an
  error): log a `warn` once per scrape and skip the affected gauge.
  Never propagate to crash the metrics handler. Prometheus tolerates
  partial scrapes.
- **Instrumentation panics**: never. Counters use `Inc()` /
  `Add()`; labels are bounded; no user-provided strings reach label
  values directly. The `cache` and `kind` labels are enums.

## Testing

`internal/metrics/metrics_test.go`:
- Construct `New()`, register a real proxy/cache/CA test double,
  exercise counter and histogram paths, scrape via `httptest.Server`
  + `promhttp.HandlerFor`, assert response body contains expected
  metric lines (Prometheus text format is stable).
- Test the nil-safe pattern: calling methods on `nil` does not panic.
- Test cert expiry collector against a CA with a known `NotAfter`,
  assert seconds gauge is within bounds.

Per-boundary tests:
- `internal/cache/cache_test.go` — add cases asserting hit/miss/bytes
  counters increment correctly when an injected `*metrics.Metrics` is
  present.
- `internal/proxy/transport_test.go` (or wherever transport errors
  surface) — assert `kind` classification for representative errors.
- `cmd/escrow-proxy/main_test.go` (if it exists) — assert metrics
  server starts on configured port and serves `/metrics` with at
  least one expected metric.

No need to retest every existing code path; the instrumentation hooks
are the new behavior under test.

## Migration / rollout

- No breaking changes. Default behavior changes only in that port
  `:9090` is now bound. Operators relying on `:9090` for something
  else will see a bind error at startup and can pass
  `--metrics-listen ""` or pick another port. Document in README under
  "Configuration" and add an entry to CHANGELOG under the next
  release-please bump.

## Open questions

None at design time. Bucket choices for the request-duration
histogram default to `prometheus.DefBuckets` and can be tuned later
based on observed traffic shapes without changing the metric name.
