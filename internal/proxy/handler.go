package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/metrics"
)

type Handler struct {
	cache           *cache.Cache
	keyHeaders      []string
	methods         map[string]bool
	excludePatterns []*regexp.Regexp
	mode            Mode
	logger          *slog.Logger
	timeout         time.Duration
	upstream        goproxy.RoundTripper
	metrics         *metrics.Metrics

	verifyDigest         bool
	digestMismatchAction DigestMismatchAction
}

// reqState is stashed in ctx.UserData across HandleRequest/HandleResponse
// so we can record the request counter + duration exactly once at the
// point we know the final outcome.
type reqState struct {
	key   string
	start time.Time
}

// modeLabel maps Mode to the Prometheus label value.
func modeLabel(m Mode) string {
	switch m {
	case ModeRecord:
		return "record"
	case ModeOffline:
		return "offline"
	default:
		return "serve"
	}
}

// recordRequest emits the per-request counter + duration once the
// outcome is known. Safe with nil h.metrics.
func (h *Handler) recordRequest(req *http.Request, statusCode int, cache string, start time.Time) {
	if h.metrics == nil {
		return
	}
	h.metrics.RecordRequest(
		modeLabel(h.mode),
		req.Method,
		strconv.Itoa(statusCode),
		cache,
		time.Since(start),
	)
}

func (h *Handler) bypass(req *http.Request) (bool, string) {
	if len(h.methods) > 0 && !h.methods[req.Method] {
		return true, "method"
	}
	url := req.URL.String()
	for _, re := range h.excludePatterns {
		if re.MatchString(url) {
			return true, "exclude_pattern"
		}
	}
	return false, ""
}

func (h *Handler) HandleRequest(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if h.upstream != nil {
		ctx.RoundTripper = h.upstream
	}

	start := time.Now()

	// goproxy's MITM loop (https.go:273) sets req.URL = nil when url.Parse
	// fails on a malformed inner request, but still calls filterRequest
	// before checking the parse error — so we must not deref req.URL.
	// Returning (req, nil) lets goproxy take its "Illegal URL" path.
	if req.URL == nil {
		h.logger.Warn("dropping request with nil URL", "method", req.Method, "host", req.Host)
		return req, nil
	}

	if bypass, reason := h.bypass(req); bypass {
		h.logger.Debug("bypass cache", "reason", reason, "method", req.Method, "url", req.URL.String())
		if h.mode == ModeOffline {
			resp := goproxy.NewResponse(req, "text/plain", http.StatusBadGateway,
				"escrow-proxy: request bypasses cache ("+reason+") and offline mode forbids upstream")
			h.recordRequest(req, http.StatusBadGateway, metrics.CacheBypass, start)
			return req, resp
		}
		// Bypass + non-offline: forward upstream, but don't cache. We
		// won't see HandleResponse (no UserData), so record now with
		// status=0 (unknown). Operators can still see bypass counts.
		h.recordRequest(req, 0, metrics.CacheBypass, start)
		return req, nil
	}

	key := ComputeCacheKey(req, h.keyHeaders)
	ctx.UserData = &reqState{key: key, start: start}

	h.logger.Debug("request", "method", req.Method, "url", req.URL.String(), "cache_key", key)

	meta, bodyRC, err := h.cache.Get(req.Context(), key)
	if err == nil {
		bodyBytes, _ := io.ReadAll(bodyRC)
		bodyRC.Close()
		// Verify the cached body still matches the digest claimed by the
		// URL before serving. A mismatch here means the entry was poisoned
		// (truncated upstream read, MITM corruption, key collision) — evict
		// it and fall through to a fresh upstream fetch. UserData remains
		// populated so HandleResponse caches the fresh response.
		if h.verifyDigest {
			if digest := ExtractOCIDigest(req.URL.Path); digest != "" && !VerifyDigest(bodyBytes, digest) {
				h.logger.Error("digest mismatch on cache hit; evicting and refetching",
					"url", req.URL.String(), "key", key, "want", digest)
				h.metrics.RecordDigestMismatch(metrics.DigestSiteCacheHit)
				_ = h.cache.Delete(req.Context(), key)
				return req, nil
			}
		}
		h.logger.Info("cache hit", "url", req.URL.String(), "key", key)
		h.metrics.RecordCacheHit(int64(len(bodyBytes)))
		resp := buildResponse(req, meta, bodyBytes)
		h.recordRequest(req, resp.StatusCode, metrics.CacheHit, start)
		// Clear UserData so HandleResponse does not also record / cache.
		ctx.UserData = nil
		return req, resp
	}

	h.metrics.RecordCacheMiss()

	if h.mode == ModeOffline {
		h.logger.Info("cache miss (offline)", "url", req.URL.String(), "key", key)
		resp := goproxy.NewResponse(req, "text/plain", http.StatusBadGateway,
			"escrow-proxy: cache miss in offline mode for "+req.URL.String())
		h.recordRequest(req, http.StatusBadGateway, metrics.CacheMiss, start)
		ctx.UserData = nil
		return req, resp
	}

	h.logger.Info("cache miss", "url", req.URL.String(), "key", key)
	return req, nil
}

func (h *Handler) HandleResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if resp == nil || ctx.UserData == nil {
		return resp
	}

	state, ok := ctx.UserData.(*reqState)
	if !ok {
		return resp
	}

	cacheOutcome := metrics.CacheMiss
	if h.mode == ModeRecord {
		cacheOutcome = metrics.CacheRecorded
	}

	// Record upstream 5xx errors. Other classes (timeout/dial/tls/etc.)
	// are recorded in the transport, before goproxy synthesizes a 502
	// for the client.
	if resp.StatusCode >= 500 {
		h.metrics.RecordUpstreamError(metrics.ErrKindUpstream5xx)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		h.logger.Debug("not caching", "status", resp.StatusCode, "url", ctx.Req.URL.String())
		h.recordRequest(ctx.Req, resp.StatusCode, cacheOutcome, state.start)
		return resp
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		h.logger.Error("reading response body", "error", err)
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		h.recordRequest(ctx.Req, resp.StatusCode, cacheOutcome, state.start)
		return resp
	}

	// Reject content whose body does not match the digest claimed by the
	// URL. We only verify when the URL itself pins content by sha256:
	// (OCI v2 blob/manifest-by-digest), so this is sound — by spec, those
	// URLs are immutable and the digest in the path equals the body's
	// SHA256. Always evict any matching cache entry as a safety net
	// against pre-existing poisoning, regardless of the configured
	// client-facing action.
	if h.verifyDigest {
		if digest := ExtractOCIDigest(ctx.Req.URL.Path); digest != "" && !VerifyDigest(bodyBytes, digest) {
			h.logger.Error("digest mismatch on upstream response; refusing to cache",
				"url", ctx.Req.URL.String(), "key", state.key, "want", digest,
				"action", h.digestMismatchActionLabel())
			h.metrics.RecordDigestMismatch(metrics.DigestSiteUpstream)
			_ = h.cache.Delete(context.Background(), state.key)
			if h.digestMismatchAction == DigestMismatchError {
				badResp := goproxy.NewResponse(ctx.Req, "text/plain", http.StatusBadGateway,
					"escrow-proxy: response body digest mismatch for "+ctx.Req.URL.String())
				h.recordRequest(ctx.Req, http.StatusBadGateway, cacheOutcome, state.start)
				return badResp
			}
			// Passthrough: serve the mismatched body to the client without caching.
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			h.recordRequest(ctx.Req, resp.StatusCode, cacheOutcome, state.start)
			return resp
		}
	}

	meta := &cache.EntryMeta{
		Method:     ctx.Req.Method,
		URL:        ctx.Req.URL.String(),
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
	}

	bgCtx := context.Background()
	if err := h.cache.Put(bgCtx, state.key, meta, bytes.NewReader(bodyBytes)); err != nil {
		h.logger.Warn("failed to cache response", "error", err, "url", ctx.Req.URL.String())
	} else {
		h.logger.Info("cached", "url", ctx.Req.URL.String(), "key", state.key, "status", resp.StatusCode)
	}

	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	h.recordRequest(ctx.Req, resp.StatusCode, cacheOutcome, state.start)
	return resp
}

func (h *Handler) digestMismatchActionLabel() string {
	if h.digestMismatchAction == DigestMismatchPassthrough {
		return "passthrough"
	}
	return "error"
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
