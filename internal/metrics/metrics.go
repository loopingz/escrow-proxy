// Package metrics provides Prometheus instrumentation for escrow-proxy.
//
// Metrics are exposed on a dedicated HTTP server (separate from the proxy
// listener) so the proxy port can never accidentally serve /metrics, and
// so metrics scraping can target an internal-only port.
//
// All Metrics methods are safe to call on a nil receiver: they become
// no-ops. This lets callers pass nil when the metrics server is disabled
// without sprinkling nil checks at every instrumentation site.
package metrics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Cache outcome label values used with RecordRequest.
const (
	CacheHit      = "hit"
	CacheMiss     = "miss"
	CacheBypass   = "bypass"
	CacheRecorded = "recorded"
)

// Upstream error kinds used with RecordUpstreamError.
const (
	ErrKindTimeout     = "timeout"
	ErrKindDial        = "dial"
	ErrKindTLS         = "tls"
	ErrKindUpstream5xx = "upstream_5xx"
	ErrKindOther       = "other"
)

// Digest mismatch sites used with RecordDigestMismatch. "upstream" is a
// fresh response from the origin; "cache_hit" is a previously stored
// entry that no longer matches its claimed digest (poisoned cache).
const (
	DigestSiteUpstream = "upstream"
	DigestSiteCacheHit = "cache_hit"
)

type Metrics struct {
	reg              *prometheus.Registry
	requests         *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	cacheHits        prometheus.Counter
	cacheMisses      prometheus.Counter
	cacheBytesServed prometheus.Counter
	upstreamErrors   *prometheus.CounterVec
	digestMismatches *prometheus.CounterVec

	reindexInProgress atomic.Int32
}

// New constructs a Metrics with a fresh custom registry (not the global
// default). Go runtime and process collectors are registered up front;
// scrape-time collectors (CA expiry, index entries) are registered via
// Register* methods once the resources they observe are available.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "escrow_proxy_requests_total",
			Help: "Total proxy requests by mode, method, HTTP status, and cache outcome.",
		}, []string{"mode", "method", "status", "cache"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "escrow_proxy_request_duration_seconds",
			Help:    "Proxy request duration in seconds by mode and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"mode", "method"}),
		cacheHits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "escrow_proxy_cache_hits_total",
			Help: "Total cache hits.",
		}),
		cacheMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "escrow_proxy_cache_misses_total",
			Help: "Total cache misses (entry not found).",
		}),
		cacheBytesServed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "escrow_proxy_cache_bytes_served_total",
			Help: "Total bytes served from cache hits.",
		}),
		upstreamErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "escrow_proxy_upstream_errors_total",
			Help: "Total upstream errors by error kind (timeout, dial, tls, upstream_5xx, other).",
		}, []string{"kind"}),
		digestMismatches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "escrow_proxy_digest_mismatches_total",
			Help: "Total responses whose body SHA256 did not match the content digest declared in the URL, by site (upstream, cache_hit).",
		}, []string{"site"}),
	}
	reg.MustRegister(
		m.requests,
		m.requestDuration,
		m.cacheHits,
		m.cacheMisses,
		m.cacheBytesServed,
		m.upstreamErrors,
		m.digestMismatches,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Registry returns the underlying *prometheus.Registry. Useful for tests
// that want to gather metrics directly without going through HTTP.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// RecordRequest increments the request counter and observes a duration.
// All four labels are required; status is the numeric HTTP status as a
// string ("200"). cache is one of CacheHit/CacheMiss/CacheBypass/
// CacheRecorded.
func (m *Metrics) RecordRequest(mode, method, status, cache string, duration time.Duration) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(mode, method, status, cache).Inc()
	m.requestDuration.WithLabelValues(mode, method).Observe(duration.Seconds())
}

// RecordCacheHit increments the hit counter and adds bytesServed to the
// bytes-served counter. Pass 0 if size is unknown.
func (m *Metrics) RecordCacheHit(bytesServed int64) {
	if m == nil {
		return
	}
	m.cacheHits.Inc()
	if bytesServed > 0 {
		m.cacheBytesServed.Add(float64(bytesServed))
	}
}

// RecordCacheMiss increments the miss counter.
func (m *Metrics) RecordCacheMiss() {
	if m == nil {
		return
	}
	m.cacheMisses.Inc()
}

// RecordUpstreamError increments the upstream error counter under the
// given kind label. Use ClassifyUpstreamError to derive the kind from a
// transport error.
func (m *Metrics) RecordUpstreamError(kind string) {
	if m == nil || kind == "" {
		return
	}
	m.upstreamErrors.WithLabelValues(kind).Inc()
}

// RecordDigestMismatch increments the digest-mismatch counter for the
// given site (DigestSiteUpstream or DigestSiteCacheHit).
func (m *Metrics) RecordDigestMismatch(site string) {
	if m == nil || site == "" {
		return
	}
	m.digestMismatches.WithLabelValues(site).Inc()
}

// SetReindexInProgress toggles the reindex_in_progress gauge.
func (m *Metrics) SetReindexInProgress(inProgress bool) {
	if m == nil {
		return
	}
	var v int32
	if inProgress {
		v = 1
	}
	m.reindexInProgress.Store(v)
}

// RegisterCAExpiry registers a scrape-time gauge that emits the seconds
// until cert.NotAfter. Safe to call with nil cert (no-op).
func (m *Metrics) RegisterCAExpiry(cert *x509.Certificate) {
	if m == nil || cert == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "escrow_proxy_ca_expiry_seconds",
		Help: "Seconds until the CA certificate's NotAfter. Negative if already expired.",
	}, func() float64 {
		return time.Until(cert.NotAfter).Seconds()
	}))
}

// IndexCounter is the function shape used by RegisterIndexEntries to
// fetch the current index entry count.
type IndexCounter func(ctx context.Context) (int64, error)

// RegisterIndexEntries registers scrape-time gauges for the SQLite index:
// escrow_proxy_index_entries and escrow_proxy_reindex_in_progress.
// counter must be non-nil; on error it logs a warning and returns NaN.
// logger may be nil.
func (m *Metrics) RegisterIndexEntries(counter IndexCounter, logger *slog.Logger) {
	if m == nil || counter == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "escrow_proxy_index_entries",
		Help: "Number of entries in the SQLite cache index.",
	}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		n, err := counter(ctx)
		if err != nil {
			if logger != nil {
				logger.Warn("metrics: index count failed", "error", err)
			}
			return math.NaN()
		}
		return float64(n)
	}))
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "escrow_proxy_reindex_in_progress",
		Help: "1 if a reindex is currently running, 0 otherwise.",
	}, func() float64 {
		return float64(m.reindexInProgress.Load())
	}))
}

// Handler returns an http.Handler serving the Prometheus text format
// from this Metrics' registry.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics disabled", http.StatusNotFound)
		})
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// StartServer binds a new HTTP server on addr and serves /metrics and
// /healthz. The returned shutdown function blocks until the server has
// stopped (or its context expires). If addr is empty, StartServer is a
// no-op and returns a no-op shutdown.
//
// Bind failures are returned synchronously; the proxy startup path
// should treat them as fatal.
func (m *Metrics) StartServer(addr string, logger *slog.Logger) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if m == nil || addr == "" {
		return noop, nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics: bind %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if logger != nil {
			logger.Info("starting metrics server", "listen", ln.Addr().String())
		}
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if logger != nil {
				logger.Error("metrics server error", "error", err)
			}
		}
	}()

	return srv.Shutdown, nil
}

// ClassifyUpstreamError maps a transport error to one of the ErrKind*
// label values. Returns "" for a nil error.
func ClassifyUpstreamError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrKindTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrKindTimeout
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return ErrKindDial
	}
	var tlsCertErr *tls.CertificateVerificationError
	if errors.As(err, &tlsCertErr) {
		return ErrKindTLS
	}
	var tlsHdrErr tls.RecordHeaderError
	if errors.As(err, &tlsHdrErr) {
		return ErrKindTLS
	}
	var unknownAuthErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthErr) {
		return ErrKindTLS
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return ErrKindTLS
	}
	return ErrKindOther
}
