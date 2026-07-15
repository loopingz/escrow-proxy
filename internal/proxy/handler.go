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

	// Invariant: only cache a HEAD that carries a real Content-Length.
	//
	// Strict OCI clients (go-containerregistry) treat a missing length (-1)
	// as fatal and do not retry, so relaying one header-less HEAD is enough
	// to fail an image push. A HEAD has no body of its own to measure, so
	// when the length is absent (or a bogus 0) we look it up from the
	// representation via resolveHeadSize.
	//
	// If it cannot be resolved, serve the response as-is but do not cache it.
	// That keeps the invariant true with no cleanup path: a stored HEAD always
	// has a valid length, so there is never a bad entry to detect later.
	if ctx.Req.Method == http.MethodHead {
		if cl, cerr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); cerr != nil || cl <= 0 {
			size, ok := h.resolveHeadSize(ctx.Req)
			if !ok {
				h.logger.Debug("HEAD Content-Length unresolved; serving as-is, not caching",
					"url", ctx.Req.URL.String())
				resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				h.recordRequest(ctx.Req, resp.StatusCode, cacheOutcome, state.start)
				return resp
			}
			resp.ContentLength = size
			resp.Header.Set("Content-Length", strconv.FormatInt(size, 10))
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
		// would report 0, which OCI push tooling rejects.
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

// maxResolveBytes caps how much of the fallback GET body resolveHeadSize will
// buffer in memory.
//
// What we resolve here is a *manifest*, not the image payload. The HEADs that
// reach this path are OCI manifest existence/size checks (go-containerregistry's
// headManifest). A manifest is the image's small JSON table of contents: it
// lists the config and layer blobs by digest and size, but is not itself the
// bytes of the image, so it is a few KB. The large payload -- the config and
// layer blobs, MBs to hundreds of MBs -- lives at separate /blobs/ URLs and is
// never fetched here.
//
// So 4 MiB never truncates a real manifest; the cap only guards the case where
// this GET unexpectedly lands on a large body (a blob) or a pathological
// response. A body over the cap is not partially counted: resolveHeadSize
// returns ok=false rather than report a truncated length, so we never pull a
// large payload just to answer a HEAD.
const maxResolveBytes = 4 << 20 // 4 MiB

// resolveHeadSize finds the Content-Length a HEAD should advertise when the
// upstream HEAD arrived without a usable one. The correct value is the octet
// length a GET for the same URL would return (RFC 9110 §8.6), obtained two
// ways, cheapest first:
//
//  1. Reuse a sibling GET already in the cache: if one exists with a positive
//     stored Content-Length, return it. No network.
//  2. Otherwise make one GET to origin, count its body, and return that length.
//     The body is cached under the GET key, so the client's own GET becomes a
//     hit and any later HEAD is answered by branch 1.
//
// Returns ok=false when the length cannot be trusted: offline or no upstream,
// GET error or non-200, body over maxResolveBytes, or a by-digest body that
// fails verification. The caller then serves the HEAD without caching it,
// rather than advertise a guessed length.
func (h *Handler) resolveHeadSize(req *http.Request) (int64, bool) {
	getReq := &http.Request{Method: http.MethodGet, URL: req.URL, Header: req.Header.Clone()}
	getKey := ComputeCacheKey(getReq, h.keyHeaders)

	if meta, rc, err := h.cache.Get(req.Context(), getKey); err == nil {
		rc.Close()
		if cl, perr := strconv.ParseInt(meta.Header.Get("Content-Length"), 10, 64); perr == nil && cl > 0 {
			return cl, true
		}
	}

	if h.mode == ModeOffline || h.upstream == nil {
		return 0, false
	}
	greq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, req.URL.String(), nil)
	if err != nil {
		return 0, false
	}
	if a := req.Header.Get("Authorization"); a != "" {
		greq.Header.Set("Authorization", a)
	}
	if a := req.Header.Get("Accept"); a != "" {
		greq.Header.Set("Accept", a)
	}
	resp, err := h.upstream.RoundTrip(greq, nil)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	// Read one past the cap so we can tell "fit under the cap" from "exceeded it".
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResolveBytes+1))
	if err != nil {
		return 0, false
	}
	if int64(len(body)) > maxResolveBytes {
		return 0, false // over the cap: only a truncated read, so don't report a length
	}
	if digest := ExtractOCIDigest(getReq.URL.Path); h.verifyDigest && digest != "" && !VerifyDigest(body, digest) {
		return 0, false // corrupt/mismatched body: don't trust its length or cache it
	}
	// Populate the GET entry so the client's own GET is a hit and later HEADs
	// reuse it via branch 1. Best-effort.
	meta := &cache.EntryMeta{
		Method:     http.MethodGet,
		URL:        getReq.URL.String(),
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		CachedAt:   h.nowTime(),
	}
	meta.Header.Set("Content-Length", strconv.Itoa(len(body)))
	meta.Header.Del("Transfer-Encoding")
	if err := h.cache.Put(context.Background(), getKey, meta, bytes.NewReader(body)); err != nil {
		h.logger.Debug("resolveHeadSize: caching GET body failed", "error", err, "url", getReq.URL.String())
	}
	return int64(len(body)), true
}
