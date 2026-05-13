package proxy

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/metrics"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

type Mode int

const (
	ModeServe   Mode = iota
	ModeRecord
	ModeOffline
)

// DigestMismatchAction selects what the proxy does when a response body's
// SHA256 disagrees with the digest claimed by the request URL. Cache
// entries are always evicted on mismatch regardless of which action is
// chosen; the action only governs what is returned to the client.
type DigestMismatchAction int

const (
	// DigestMismatchError returns HTTP 502 to the client and refuses to
	// cache the mismatched body. Default.
	DigestMismatchError DigestMismatchAction = iota
	// DigestMismatchPassthrough serves the mismatched body to the client
	// (so the client's own integrity check can catch it) but refuses to
	// cache it.
	DigestMismatchPassthrough
)

type Config struct {
	Mode            Mode
	Cache           *cache.Cache
	CertCache       *tlspkg.CertCache
	CA              *tlspkg.CA
	KeyHeaders      []string
	Methods         []string
	ExcludePatterns []*regexp.Regexp
	UpstreamTimeout time.Duration
	Logger          *slog.Logger
	AllowFallback   bool
	Metrics         *metrics.Metrics // optional; nil disables instrumentation

	// VerifyDigest enables SHA256 verification of response bodies whose
	// URL pins content by digest (OCI v2 /blobs/sha256:<hex> and
	// /manifests/sha256:<hex>). Mismatched bodies are never cached and
	// any matching existing cache entry is evicted.
	VerifyDigest bool
	// DigestMismatchAction governs the client-facing behavior when a
	// mismatch is detected on a fresh upstream response. Has no effect
	// on the cache-hit path, which always evicts and retries upstream.
	DigestMismatchAction DigestMismatchAction
}

func New(cfg *Config) *goproxy.ProxyHttpServer {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = cfg.Logger != nil && cfg.Logger.Enabled(nil, slog.LevelDebug)

	tlsCfg := func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
		cert, err := cfg.CertCache.GetOrCreate(host)
		if err != nil {
			return nil, err
		}
		return &tls.Config{
			Certificates: []tls.Certificate{*cert},
		}, nil
	}

	proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			return &goproxy.ConnectAction{
				Action:    goproxy.ConnectMitm,
				TLSConfig: tlsCfg,
			}, host
		},
	))

	methods := make(map[string]bool, len(cfg.Methods))
	for _, m := range cfg.Methods {
		methods[m] = true
	}

	handler := &Handler{
		cache:                cfg.Cache,
		keyHeaders:           cfg.KeyHeaders,
		methods:              methods,
		excludePatterns:      cfg.ExcludePatterns,
		mode:                 cfg.Mode,
		logger:               cfg.Logger,
		timeout:              cfg.UpstreamTimeout,
		metrics:              cfg.Metrics,
		upstream:             newRedirectFollower(http.DefaultTransport, cfg.Metrics),
		verifyDigest:         cfg.VerifyDigest,
		digestMismatchAction: cfg.DigestMismatchAction,
	}

	proxy.OnRequest().DoFunc(handler.HandleRequest)
	proxy.OnResponse().DoFunc(handler.HandleResponse)

	return proxy
}
