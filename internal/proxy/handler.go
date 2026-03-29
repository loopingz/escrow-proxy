package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
)

type Handler struct {
	cache      *cache.Cache
	keyHeaders []string
	mode       Mode
	logger     *slog.Logger
	timeout    time.Duration
}

func (h *Handler) HandleRequest(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	key := ComputeCacheKey(req, h.keyHeaders)
	ctx.UserData = key

	h.logger.Debug("request", "method", req.Method, "url", req.URL.String(), "cache_key", key)

	meta, bodyRC, err := h.cache.Get(req.Context(), key)
	if err == nil {
		defer bodyRC.Close()
		bodyBytes, _ := io.ReadAll(bodyRC)
		h.logger.Info("cache hit", "url", req.URL.String(), "key", key)
		return req, buildResponse(req, meta, bodyBytes)
	}

	if h.mode == ModeOffline {
		h.logger.Info("cache miss (offline)", "url", req.URL.String(), "key", key)
		return req, goproxy.NewResponse(req, "text/plain", http.StatusBadGateway,
			"escrow-proxy: cache miss in offline mode for "+req.URL.String())
	}

	h.logger.Info("cache miss", "url", req.URL.String(), "key", key)
	return req, nil
}

func (h *Handler) HandleResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if resp == nil || ctx.UserData == nil {
		return resp
	}

	key, ok := ctx.UserData.(string)
	if !ok {
		return resp
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		h.logger.Debug("not caching", "status", resp.StatusCode, "url", ctx.Req.URL.String())
		return resp
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		h.logger.Error("reading response body", "error", err)
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		return resp
	}

	meta := &cache.EntryMeta{
		Method:     ctx.Req.Method,
		URL:        ctx.Req.URL.String(),
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
	}

	bgCtx := context.Background()
	if err := h.cache.Put(bgCtx, key, meta, bytes.NewReader(bodyBytes)); err != nil {
		h.logger.Warn("failed to cache response", "error", err, "url", ctx.Req.URL.String())
	} else {
		h.logger.Info("cached", "url", ctx.Req.URL.String(), "key", key, "status", resp.StatusCode)
	}

	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return resp
}

func buildResponse(req *http.Request, meta *cache.EntryMeta, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    meta.StatusCode,
		Status:        http.StatusText(meta.StatusCode),
		Header:        meta.Header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
	}
}
