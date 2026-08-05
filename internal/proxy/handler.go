package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/metrics"
)

type Handler struct {
	cache              *cache.Cache
	keyHeaders         []string
	methods            map[string]bool
	excludePatterns    []*regexp.Regexp
	revalidatePatterns []*regexp.Regexp
	revalidateInterval time.Duration
	// now is the clock source for revalidation freshness checks. Always
	// time.Now in production; tests override to control "elapsed since
	// CachedAt" without sleeping.
	now      func() time.Time
	mode     Mode
	logger   *slog.Logger
	timeout  time.Duration
	upstream goproxy.RoundTripper
	metrics  *metrics.Metrics

	verifyDigest         bool
	digestMismatchAction DigestMismatchAction
}

// reqState is stashed in ctx.UserData across HandleRequest/HandleResponse
// so we can record the request counter + duration exactly once at the
// point we know the final outcome.
type reqState struct {
	key   string
	start time.Time
	// fallback holds the stale cached body when a revalidate-matching URL
	// triggered an upstream refresh. If upstream returns non-2xx (or never
	// responds), HandleResponse serves these bytes instead, leaving the
	// cache entry untouched so the next request retries upstream.
	fallback     []byte
	fallbackMeta *cache.EntryMeta
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

// nowTime returns the handler clock, defaulting to time.Now when the
// Handler was constructed without one (production always sets it via
// proxy.New; direct construction in tests may not).
func (h *Handler) nowTime() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// needsRevalidation reports whether a cache hit for url is too old to
// serve directly: the URL matches a revalidate pattern and the entry's
// CachedAt is at least revalidateInterval ago. A zero CachedAt (legacy
// entry written before the field existed) is immediately stale.
func (h *Handler) needsRevalidation(url string, meta *cache.EntryMeta) bool {
	if h.revalidateInterval <= 0 {
		return false
	}
	for _, re := range h.revalidatePatterns {
		if re.MatchString(url) {
			return h.nowTime().Sub(meta.CachedAt) >= h.revalidateInterval
		}
	}
	return false
}

func (h *Handler) bypass(req *http.Request) (bool, string) {
	if len(h.methods) > 0 && !h.methods[req.Method] {
		return true, "method"
	}
	url := normalizeMatchURL(req.URL)
	for _, re := range h.excludePatterns {
		if re.MatchString(url) {
			return true, "exclude_pattern"
		}
	}
	return false, ""
}

// normalizeMatchURL renders a URL for exclude-pattern matching with the
// default port elided. goproxy's MITM path reconstructs req.URL with an
// explicit ":443" (or ":80"), so a host-anchored pattern like
// `cgr\.dev/token` would otherwise never match `cgr.dev:443/token` and the
// (per-scope, short-lived) bearer token would be cached and served stale.
// Patterns are written against the canonical host without the default port.
func normalizeMatchURL(u *url.URL) string {
	if port := u.Port(); (u.Scheme == "https" && port == "443") ||
		(u.Scheme == "http" && port == "80") {
		clone := *u
		clone.Host = u.Hostname()
		return clone.String()
	}
	return u.String()
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
	state := &reqState{key: key, start: start}
	ctx.UserData = state

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
		// HEAD responses carry no body by HTTP spec, so there's nothing to
		// hash — skip verification. OCI clients use HEAD on by-digest URLs
		// to probe blob existence; verifying an empty body would always
		// fail and turn every HEAD into a spurious 502.
		if h.verifyDigest && req.Method != http.MethodHead {
			if digest := ExtractOCIDigest(req.URL.Path); digest != "" && !VerifyDigest(bodyBytes, digest) {
				h.logger.Error("digest mismatch on cache hit; evicting and refetching",
					"url", req.URL.String(), "key", key, "want", digest)
				h.metrics.RecordDigestMismatch(metrics.DigestSiteCacheHit)
				_ = h.cache.Delete(req.Context(), key)
				return req, nil
			}
		}
		// Stale entry on a revalidate-matching URL: defer to upstream for
		// a fresh copy, keeping the cached body as a fallback in case
		// upstream fails. Offline mode has no upstream, so staleness is
		// ignored there and the cached body served as-is.
		if h.mode != ModeOffline && h.needsRevalidation(req.URL.String(), meta) {
			h.logger.Info("cache stale; revalidating upstream",
				"url", req.URL.String(), "key", key, "cached_at", meta.CachedAt)
			state.fallback = bodyBytes
			state.fallbackMeta = meta
			return req, nil
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

// serveFallback answers with the stale cached body stashed by the
// revalidation path. The cache entry (and its CachedAt) is deliberately
// left untouched so the next request retries upstream immediately.
func (h *Handler) serveFallback(state *reqState, ctx *goproxy.ProxyCtx, upstreamStatus int) *http.Response {
	h.logger.Warn("revalidation failed; serving stale cached copy",
		"url", ctx.Req.URL.String(), "key", state.key, "upstream_status", upstreamStatus)
	resp := buildResponse(ctx.Req, state.fallbackMeta, state.fallback)
	h.recordRequest(ctx.Req, resp.StatusCode, metrics.CacheHit, state.start)
	return resp
}

func (h *Handler) HandleResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if ctx.UserData == nil {
		return resp
	}

	state, ok := ctx.UserData.(*reqState)
	if !ok {
		return resp
	}

	if resp == nil {
		// Defensive: the production transport synthesizes a 502 on
		// upstream errors precisely so this branch is never taken (goproxy's
		// MITM loop skips filterResponse on RoundTrip errors). With a
		// revalidation fallback in hand, serve the stale copy instead of
		// letting goproxy synthesize an error for the client.
		if state.fallbackMeta != nil {
			return h.serveFallback(state, ctx, 0)
		}
		return resp
	}

	// Responses synthesized by the transport for upstream failures carry a
	// marker header: strip it (internal detail) and skip the 5xx metric
	// below — the transport already recorded the classified error.
	upstreamErr := resp.Header.Get(upstreamErrorHeader) != ""
	if upstreamErr {
		resp.Header.Del(upstreamErrorHeader)
	}

	// Debug-log the full framing when a 2xx we may serve/cache has an absent or
	// non-positive Content-Length. go-containerregistry's headManifest treats a
	// missing length (ContentLength == -1) as fatal and does not retry, so a
	// single such response fails an image push; capturing protocol, encoding,
	// redirect chain, and backend fingerprints makes the cause (HTTP/2 omission,
	// chunked transfer, transparent gzip, or a redirect to a storage backend)
	// diagnosable in production without a local repro.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if cl, perr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); perr != nil || cl <= 0 {
			finalHost := ""
			redirected := false
			if resp.Request != nil && resp.Request.URL != nil {
				redirected = resp.Request.URL.String() != ctx.Req.URL.String()
				if redirected {
					// Host only. A redirect target is frequently a signed
					// storage/CDN URL whose query string carries short-lived
					// credentials; never log the full URL. The host alone answers
					// "did it redirect to a storage backend" without leaking them.
					finalHost = resp.Request.URL.Host
				}
			}
			h.logger.Debug("upstream 2xx missing usable Content-Length",
				"method", ctx.Req.Method,
				"url", ctx.Req.URL.String(),
				"status", resp.StatusCode,
				"proto", resp.Proto,
				// request-arrival-to-response; dominated by the upstream fetch, so
				// a high value correlates the omission with a slow/stressed hop.
				"fetch_ms", time.Since(state.start).Milliseconds(),
				"cl_header", resp.Header.Get("Content-Length"),
				"cl_field", resp.ContentLength,
				"content_encoding", resp.Header.Get("Content-Encoding"),
				"transfer_encoding", resp.TransferEncoding,
				"uncompressed", resp.Uncompressed,
				"redirected", redirected,
				"final_host", finalHost,
				"server", resp.Header.Get("Server"),
				"via", resp.Header.Get("Via"))
		}
	}

	cacheOutcome := metrics.CacheMiss
	if h.mode == ModeRecord {
		cacheOutcome = metrics.CacheRecorded
	}

	// Record upstream 5xx errors. Other classes (timeout/dial/tls/etc.)
	// are recorded in the transport, before it synthesizes a 502 for the
	// client.
	if resp.StatusCode >= 500 && !upstreamErr {
		h.metrics.RecordUpstreamError(metrics.ErrKindUpstream5xx)
	}

	// Revalidation: only a 2xx refresh replaces the cached copy; any
	// other status (3xx redirects included) serves the stale fallback.
	if state.fallbackMeta != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		resp.Body.Close()
		return h.serveFallback(state, ctx, resp.StatusCode)
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
		// A truncated 2xx refresh is just another upstream failure for
		// the revalidation path: serve the intact stale copy instead of
		// the partial body.
		if state.fallbackMeta != nil {
			return h.serveFallback(state, ctx, resp.StatusCode)
		}
		setBufferedBody(resp, bodyBytes)
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
	if h.verifyDigest && ctx.Req.Method != http.MethodHead {
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

	// Invariant: only cache a HEAD that carries a real Content-Length. Strict
	// OCI clients (go-containerregistry) treat a missing length (-1) as fatal
	// with no retry, so a single header-less HEAD fails an image push. If the
	// upstream HEAD didn't carry one, serve as-is and skip caching -- that way
	// a stored HEAD is always valid, with no cleanup path needed.
	//
	// A usable length isn't enough by itself, though: goproxy's MITM writer
	// deletes Content-Length and forces chunked framing whenever resp.Body's
	// identity changes across this handler. So below, a HEAD's resp.Body is
	// never reassigned (it has no body to give the client anyway) -- that
	// keeps goproxy from tripping on it and stripping what the origin sent.
	if ctx.Req.Method == http.MethodHead {
		if cl, cerr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); cerr != nil || cl <= 0 {
			h.logger.Debug("HEAD Content-Length missing; serving as-is, not caching",
				"url", ctx.Req.URL.String())
			h.recordRequest(ctx.Req, resp.StatusCode, cacheOutcome, state.start)
			return resp
		}
	}

	meta := &cache.EntryMeta{
		Method:     ctx.Req.Method,
		URL:        ctx.Req.URL.String(),
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		CachedAt:   h.nowTime(),
	}

	bgCtx := context.Background()
	if err := h.cache.Put(bgCtx, state.key, meta, bytes.NewReader(bodyBytes)); err != nil {
		h.logger.Warn("failed to cache response", "error", err, "url", ctx.Req.URL.String())
	} else {
		h.logger.Info("cached", "url", ctx.Req.URL.String(), "key", state.key,
			"status", resp.StatusCode,
			"content_length", resp.ContentLength, // upstream-advertised length (-1 == none)
			"body_bytes", len(bodyBytes), // actual buffered body size (0 for HEAD)
			"content_encoding", resp.Header.Get("Content-Encoding"),
			"transfer_encoding", resp.TransferEncoding,
			"proto", resp.Proto)
	}

	// See the goproxy note above: a HEAD's Body must stay unreassigned.
	if ctx.Req.Method != http.MethodHead {
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}
	h.recordRequest(ctx.Req, resp.StatusCode, cacheOutcome, state.start)
	return resp
}

func (h *Handler) digestMismatchActionLabel() string {
	if h.digestMismatchAction == DigestMismatchPassthrough {
		return "passthrough"
	}
	return "error"
}

// setBufferedBody re-serves resp from a fully-read, in-memory buffer. Because
// the body length is now known, it pins Content-Length and drops any chunked
// framing inherited from upstream. Without this, a manifest or blob the
// registry returned with Transfer-Encoding: chunked would be relayed without a
// Content-Length header, which strict OCI clients (e.g. go-containerregistry)
// reject with "response did not include Content-Length header".
func setBufferedBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.TransferEncoding = nil
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Transfer-Encoding")
}

func buildResponse(req *http.Request, meta *cache.EntryMeta, body []byte) *http.Response {
	header := meta.Header.Clone()
	contentLength := int64(len(body))
	if req.Method == http.MethodHead {
		// A HEAD has no body but its Content-Length still advertises the size a
		// GET would return. Keep that header value; deriving it from len(body)
		// would report 0 -- a false size, not one go-containerregistry rejects
		// (its only check is ContentLength == -1), but still not truthful.
		if cl, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64); err == nil {
			contentLength = cl
		} else {
			// Origin sent no Content-Length; leave it unset rather than assert 0.
			contentLength = -1
		}
	} else {
		// Same rationale as setBufferedBody: a cache hit is served from a
		// known-size buffer, so pin Content-Length to the body length.
		header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	// Drop any chunked framing recorded in the cached header — the body is
	// served whole from an in-memory buffer.
	header.Del("Transfer-Encoding")
	return &http.Response{
		StatusCode:    meta.StatusCode,
		Status:        http.StatusText(meta.StatusCode),
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: contentLength,
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
	}
}
