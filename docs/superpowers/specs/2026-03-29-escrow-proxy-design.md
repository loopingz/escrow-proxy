# Escrow Proxy — Design Specification

**Date:** 2026-03-29
**Status:** Draft

## Overview

Escrow Proxy is a Go-based MITM HTTP/HTTPS proxy for CI/CD dependency caching. It intercepts TLS traffic, caches responses by content digest in tiered storage (local + cloud), and supports three operating modes: normal caching (`serve`), session recording to portable archives (`record`), and offline replay from archives (`offline`).

## Goals

- Cache package downloads (npm, Maven, pip, Docker images, etc.) to avoid hitting external registries repeatedly
- Enable offline/air-gapped builds from recorded archives
- Support tiered storage: fast local L1 with cloud L2 (GCS, S3)
- Portable archives in multiple formats (tar.gz, OCI image, custom CAS)

## Architecture

![Architecture Diagram](./escrow-proxy-architecture.excalidraw.svg)

### Layers

1. **Proxy Engine** — MITM HTTP/HTTPS proxy using `elazarl/goproxy`. Intercepts CONNECT requests, performs TLS interception with dynamically generated certificates, delegates to the cache layer.

2. **Cache Layer** — Content-addressed storage keyed by a digest of (URL + method + configurable headers + body hash). Tiered: checks L1 (local) first, falls back to L2 (remote: GCS or S3). Writes propagate to all tiers.

3. **Archive Layer** — Bundles cached request/response pairs into a portable archive. Three formats: tar.gz, OCI image (with built-in registry push/pull), and a custom CAS format (index + blobs).

4. **CLI** — Three subcommands: `serve`, `record`, `offline`, plus `ca export`.

### Request Flow (`serve` mode)

```
Client → CONNECT → MITM TLS → Parse Request
  → Compute cache key (digest)
  → Check L1 (local) → hit? return cached response
  → Check L2 (GCS/S3) → hit? store in L1, return cached response
  → Miss: forward upstream → store response in L1 + L2 → return to client
```

### `record` mode

Same as `serve`, but additionally tracks each (key, request, response) in a session manifest. On shutdown (or explicit signal), bundles the manifest into the chosen archive format.

### `offline` mode

Loads an archive into a local index. All lookups go against that index only. Returns HTTP 502 on miss, unless `--allow-fallback` is set (in which case, falls through to live upstream).

## Storage Abstraction

### Interface

```go
type Storage interface {
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Put(ctx context.Context, key string, r io.Reader) error
    Exists(ctx context.Context, key string) (bool, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]string, error)
}
```

### Cached Entry Format

Each cached entry is stored as two objects under the key digest:
- `{digest}.meta` — JSON containing request URL, method, response status code, response headers
- `{digest}.body` — raw response body

### Implementations

- **Local** — files on disk under a configurable directory (default: `~/.escrow-proxy/cache/`)
- **GCS** — `cloud.google.com/go/storage`, configured with bucket + optional prefix
- **S3** — `aws-sdk-go-v2`, configured with bucket + prefix + region

### Tiered Storage

```go
type TieredStorage struct {
    Tiers []Storage // ordered L1 → L2 → ...
}
```

- **Get**: check tiers in order. On hit at tier N, backfill all tiers < N (promote to faster tiers).
- **Put**: write to all tiers concurrently.
- **Delete**: delete from all tiers.
- **Exists**: check tiers in order, return on first hit.

### Cache Key Computation

```
key = SHA256(method + "\n" + url + "\n" + sorted(selected_headers) + "\n" + body_hash)
```

Where:
- `selected_headers` is configurable (default: `Accept`, `Accept-Encoding`)
- `body_hash` is SHA256 of the request body (empty string hash if no body)

## Archive Abstraction

### Interfaces

```go
type ArchiveWriter interface {
    Add(ctx context.Context, key string, meta []byte, body io.Reader) error
    Close() error
}

type ArchiveReader interface {
    Get(ctx context.Context, key string) ([]byte, io.ReadCloser, error)
    List(ctx context.Context) ([]string, error)
    Close() error
}

type ArchiveFormat interface {
    NewWriter(dest string) (ArchiveWriter, error)
    NewReader(src string) (ArchiveReader, error)
}
```

### tar.gz Format

Flat archive:
```
archive.tar.gz
├── index.json          # {key: {meta_offset, body_offset}}
├── {digest1}.meta
├── {digest1}.body
├── {digest2}.meta
├── {digest2}.body
└── ...
```

### OCI Image Format

Entries are grouped into chunk layers to avoid layer limits. Default: 1000 entries per layer (`--oci-entries-per-layer`).

```
OCI layout:
├── oci-layout
├── index.json
└── blobs/
    └── sha256/
        ├── {manifest}
        ├── {config}       # full index: key → layer + offset
        ├── {layer1}       # tar of N entries (meta + body pairs)
        └── {layer2}
```

Built-in push/pull to container registries using `oras-go`. Supports standard registry auth (docker config, env vars).

### Custom CAS Format

Content-addressed directory, easy to inspect and diff:
```
cas/
├── index.json          # key → {meta_digest, body_digest, url, method}
├── blobs/
│   └── sha256/
│       ├── abc123...
│       └── def456...
└── meta/
    ├── {key1}.json
    └── {key2}.json
```

### Format Detection

Archive destination determines format:
- `./output.tar.gz` → tar.gz
- `./output/` → CAS directory
- `registry.example.com/repo:tag` → OCI push to registry

Explicit override via `--format={tgz,oci,cas}`.

## TLS & Certificate Management

### CA Initialization

- On first run with no CA configured: generate root CA (ECDSA P-256, 10-year validity), store in `~/.escrow-proxy/ca/`
- User can provide their own CA via `--ca-cert` and `--ca-key` flags or config file
- CA cert path printed at startup for trust store setup

### Per-Host Certificate Generation

- Dynamic leaf certificate generation on CONNECT, signed by the CA
- In-memory LRU cache keyed by hostname
- Short validity (24h) — certificates are ephemeral

### Helper

`escrow-proxy ca export` prints the CA cert PEM to stdout for piping into trust store setup scripts.

## CLI & Configuration

### Subcommands

```
escrow-proxy serve       # Normal caching proxy
escrow-proxy record      # Cache + build archive on shutdown
escrow-proxy offline     # Serve from archive only
escrow-proxy ca export   # Print CA cert PEM to stdout
```

### Common Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Path to config file (YAML) |
| `--listen` / `-l` | `:8080` | Bind address |
| `--ca-cert` / `--ca-key` | — | Custom CA certificate and key |
| `--cache-key-headers` | `Accept,Accept-Encoding` | Headers included in cache key |
| `--log-level` | `info` | Log level: debug, info, warn, error |

### Storage Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--storage` | `local` | Comma-separated tier list (e.g., `local,gcs`) |
| `--local-dir` | `~/.escrow-proxy/cache/` | Local cache directory |
| `--gcs-bucket` / `--gcs-prefix` | — | GCS configuration |
| `--s3-bucket` / `--s3-prefix` / `--s3-region` | — | S3 configuration |

### Record Mode Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--output` / `-o` | — | Archive destination (path or registry ref) |
| `--format` | auto-detect | `tgz`, `oci`, `cas` |
| `--oci-entries-per-layer` | `1000` | Entries per OCI layer |

### Offline Mode Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--archive` / `-a` | — | Archive source (path or registry ref) |
| `--allow-fallback` | `false` | On miss, forward upstream instead of 502 |

### Config File (YAML)

```yaml
listen: ":8080"
ca:
  cert: /path/to/ca.crt
  key: /path/to/ca.key
cache:
  key_headers: ["Accept", "Accept-Encoding"]
storage:
  tiers:
    - type: local
      dir: /tmp/escrow-cache
    - type: gcs
      bucket: my-bucket
      prefix: escrow/
record:
  output: registry.example.com/cache:latest
  format: oci
  oci_entries_per_layer: 1000
offline:
  archive: registry.example.com/cache:latest
  allow_fallback: false
```

CLI flags override config file values.

## Error Handling & Edge Cases

- **Upstream failures**: 5xx responses are not cached. Only 2xx and 3xx responses are stored.
- **Storage failures**: L2 write failure still serves the response from L1. Logged as warning.
- **All tiers fail on read**: treat as cache miss, forward upstream (`serve`/`record`).
- **Large responses**: stream via `io.TeeReader` — no full buffering in memory.
- **Concurrent duplicate requests**: `singleflight` deduplication — one upstream request, shared response.
- **Graceful shutdown**: on SIGINT/SIGTERM, finish in-flight requests, flush pending writes, finalize archive (`record` mode), then exit.
- **Archive load failure** (`offline`): fail fast at startup with clear error.
- **Archive write failure** (`record`): exit non-zero with error message.
- **Upstream timeout**: configurable via `--upstream-timeout` (default: 30s).

## Project Structure

```
escrow-proxy/
├── cmd/
│   └── escrow-proxy/
│       └── main.go              # CLI entrypoint (cobra)
├── internal/
│   ├── proxy/
│   │   ├── proxy.go             # MITM proxy engine setup
│   │   ├── handler.go           # Request/response interception logic
│   │   └── cache_key.go         # Cache key computation
│   ├── cache/
│   │   ├── cache.go             # Cache layer (get/put with storage)
│   │   └── entry.go             # Meta + body types
│   ├── storage/
│   │   ├── storage.go           # Storage interface
│   │   ├── tiered.go            # TieredStorage implementation
│   │   ├── local.go             # Local filesystem backend
│   │   ├── gcs.go               # GCS backend
│   │   └── s3.go                # S3 backend
│   ├── archive/
│   │   ├── archive.go           # ArchiveFormat/Reader/Writer interfaces
│   │   ├── targz.go             # tar.gz implementation
│   │   ├── oci.go               # OCI image implementation
│   │   ├── oci_registry.go      # OCI push/pull to registries
│   │   └── cas.go               # Custom CAS implementation
│   ├── tls/
│   │   ├── ca.go                # CA generation and loading
│   │   └── certs.go             # Dynamic leaf cert generation + LRU cache
│   └── config/
│       └── config.go            # YAML config parsing + CLI merge
├── go.mod
├── go.sum
└── docs/
```

## Testing Strategy

### Unit Tests

- **storage/** — interface contract test suite run against all backends. Local uses temp dirs; GCS and S3 use emulators (`fsouza/fake-gcs-server`, S3Mock or similar).
- **archive/** — round-trip tests: write entries, read back, verify content. Each format tested independently.
- **cache/** — key computation determinism, tiered promotion (L2 hit backfills L1).
- **tls/** — CA generation, leaf cert generation, cert LRU caching.
- **proxy/** — handler logic with `httptest` servers as upstream.

### Integration Tests

- Full proxy startup with test upstream (`httptest` TLS server), make requests through proxy, verify caching.
- All three modes: `serve` (caches), `record` (produces archive), `offline` (serves from archive).
- Round-trip test: `record` a session → `offline` replay from produced archive → verify identical responses.

### No mocks for storage interface in integration tests

Use real local storage and emulators for cloud backends.

## Key Dependencies

- `elazarl/goproxy` — MITM proxy engine
- `spf13/cobra` — CLI framework
- `cloud.google.com/go/storage` — GCS client
- `aws-sdk-go-v2` — S3 client
- `oras-go` — OCI registry push/pull
- `golang.org/x/sync/singleflight` — request deduplication
