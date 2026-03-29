package proxy

import (
	"crypto/tls"
	"log/slog"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

type Mode int

const (
	ModeServe   Mode = iota
	ModeRecord
	ModeOffline
)

type Config struct {
	Mode            Mode
	Cache           *cache.Cache
	CertCache       *tlspkg.CertCache
	CA              *tlspkg.CA
	KeyHeaders      []string
	UpstreamTimeout time.Duration
	Logger          *slog.Logger
	AllowFallback   bool
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

	handler := &Handler{
		cache:      cfg.Cache,
		keyHeaders: cfg.KeyHeaders,
		mode:       cfg.Mode,
		logger:     cfg.Logger,
		timeout:    cfg.UpstreamTimeout,
	}

	proxy.OnRequest().DoFunc(handler.HandleRequest)
	proxy.OnResponse().DoFunc(handler.HandleResponse)

	return proxy
}
