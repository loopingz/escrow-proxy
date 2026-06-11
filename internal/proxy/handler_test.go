package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/metrics"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// newRevalidateHandler returns a Handler wired with a real local-storage
// Cache, a configurable clock, and the revalidate pattern matching
// "/simple/" with a 5-minute interval. Most revalidate tests just vary the
// clock and the cached entry's age to exercise fresh vs. stale paths.
func newRevalidateHandler(t *testing.T, now func() time.Time) (*Handler, *cache.Cache) {
	t.Helper()
	c := cache.New(storage.NewLocal(t.TempDir()))
	h := &Handler{
		cache:              c,
		methods:            map[string]bool{"GET": true, "HEAD": true},
		revalidatePatterns: []*regexp.Regexp{regexp.MustCompile(`/simple/`)},
		revalidateInterval: 5 * time.Minute,
		now:                now,
		mode:               ModeServe,
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics:            metrics.New(),
	}
	return h, c
}

func seedCache(t *testing.T, c *cache.Cache, h *Handler, reqURL, body string, cachedAt time.Time) string {
	t.Helper()
	req, _ := http.NewRequest("GET", reqURL, nil)
	key := ComputeCacheKey(req, h.keyHeaders)
	meta := &cache.EntryMeta{
		Method:     "GET",
		URL:        reqURL,
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/html"}},
		CachedAt:   cachedAt,
	}
	if err := c.Put(context.Background(), key, meta, bytes.NewReader([]byte(body))); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	return key
}

func mkReq(t *testing.T, method, raw string) *http.Request {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return &http.Request{
		Method: method,
		URL:    u,
		Host:   u.Host,
		Header: http.Header{},
	}
}

func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return b
}

// Revalidate URL + cache entry younger than the interval: serve cached,
// never reach upstream. (Returned response is non-nil; goproxy treats
// non-nil response as "done, skip upstream".)
func TestRevalidate_FreshCacheHit_ServesCachedSkipsUpstream(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	// Cached 1 min ago — well within the 5 min interval.
	seedCache(t, c, h, "https://pypi.example/simple/urllib3/", "OLD-INDEX-BODY",
		now.Add(-1*time.Minute))

	req := mkReq(t, "GET", "https://pypi.example/simple/urllib3/")
	ctx := &goproxy.ProxyCtx{Req: req}
	gotReq, resp := h.HandleRequest(req, ctx)
	if gotReq != req {
		t.Fatalf("returned request: got %p, want %p", gotReq, req)
	}
	if resp == nil {
		t.Fatal("expected cached response, got nil (would have hit upstream)")
	}
	if string(readAll(t, resp.Body)) != "OLD-INDEX-BODY" {
		t.Fatal("served body did not match cached body")
	}
	if ctx.UserData != nil {
		t.Fatalf("UserData should be cleared on direct cache hit, got %v", ctx.UserData)
	}
}

// Revalidate URL + cache entry older than the interval: trigger upstream
// (return nil response) and stash the cached body in reqState so
// HandleResponse can fall back if upstream isn't 2xx.
func TestRevalidate_StaleCacheHit_LetsUpstreamRunWithFallback(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	// Cached 10 min ago — older than the 5 min interval.
	seedCache(t, c, h, "https://pypi.example/simple/urllib3/", "OLD-INDEX-BODY",
		now.Add(-10*time.Minute))

	req := mkReq(t, "GET", "https://pypi.example/simple/urllib3/")
	ctx := &goproxy.ProxyCtx{Req: req}
	_, resp := h.HandleRequest(req, ctx)
	if resp != nil {
		t.Fatalf("stale entry should defer to upstream, got cached response %v", resp)
	}
	state, ok := ctx.UserData.(*reqState)
	if !ok || state == nil {
		t.Fatalf("expected reqState in UserData, got %T", ctx.UserData)
	}
	if string(state.fallback) != "OLD-INDEX-BODY" {
		t.Fatalf("fallback bytes: got %q, want OLD-INDEX-BODY", state.fallback)
	}
	if state.fallbackMeta == nil || state.fallbackMeta.StatusCode != 200 {
		t.Fatalf("fallbackMeta not populated correctly: %+v", state.fallbackMeta)
	}
}

// Stale entry + upstream 2xx: HandleResponse caches the fresh response
// (with refreshed CachedAt) and serves the upstream body — no fallback.
func TestRevalidate_Stale_Upstream2xx_RefreshesCache(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	reqURL := "https://pypi.example/simple/urllib3/"
	key := seedCache(t, c, h, reqURL, "OLD-INDEX-BODY", now.Add(-10*time.Minute))

	req := mkReq(t, "GET", reqURL)
	ctx := &goproxy.ProxyCtx{Req: req}
	if _, resp := h.HandleRequest(req, ctx); resp != nil {
		t.Fatal("stale path should defer to upstream")
	}

	// Simulate fresh upstream response.
	upstream := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/html"}},
		Body:       io.NopCloser(bytes.NewReader([]byte("NEW-INDEX-BODY"))),
		Request:    req,
	}
	got := h.HandleResponse(upstream, ctx)
	if got.StatusCode != 200 {
		t.Fatalf("status: got %d, want 200", got.StatusCode)
	}
	if string(readAll(t, got.Body)) != "NEW-INDEX-BODY" {
		t.Fatal("expected new upstream body served, not fallback")
	}

	gotMeta, gotBody, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("cache lookup after refresh: %v", err)
	}
	defer gotBody.Close()
	if !gotMeta.CachedAt.Equal(now) {
		t.Fatalf("CachedAt not refreshed: got %v, want %v", gotMeta.CachedAt, now)
	}
	if string(readAll(t, gotBody)) != "NEW-INDEX-BODY" {
		t.Fatal("cache body not refreshed")
	}
}

// Stale entry + upstream 5xx: serve the stale cached body and DO NOT
// update CachedAt (so the next request retries upstream immediately).
func TestRevalidate_Stale_Upstream5xx_ServesFallback(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	reqURL := "https://pypi.example/simple/urllib3/"
	originalCachedAt := now.Add(-10 * time.Minute)
	key := seedCache(t, c, h, reqURL, "OLD-INDEX-BODY", originalCachedAt)

	req := mkReq(t, "GET", reqURL)
	ctx := &goproxy.ProxyCtx{Req: req}
	h.HandleRequest(req, ctx)

	upstream := &http.Response{
		StatusCode: 503,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte("upstream is sad"))),
		Request:    req,
	}
	got := h.HandleResponse(upstream, ctx)
	if got.StatusCode != 200 {
		t.Fatalf("expected fallback to overwrite status to 200, got %d", got.StatusCode)
	}
	if string(readAll(t, got.Body)) != "OLD-INDEX-BODY" {
		t.Fatal("expected cached body served as fallback")
	}

	gotMeta, gotBody, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("cache lookup: %v", err)
	}
	defer gotBody.Close()
	if !gotMeta.CachedAt.Equal(originalCachedAt) {
		t.Fatalf("CachedAt should not be updated on fallback; got %v, want %v",
			gotMeta.CachedAt, originalCachedAt)
	}
}

// Stale entry + upstream 4xx: same fallback behavior (user spec: anything
// not 2xx is upstream failure).
func TestRevalidate_Stale_Upstream4xx_ServesFallback(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	reqURL := "https://pypi.example/simple/urllib3/"
	seedCache(t, c, h, reqURL, "OLD-INDEX-BODY", now.Add(-10*time.Minute))

	req := mkReq(t, "GET", reqURL)
	ctx := &goproxy.ProxyCtx{Req: req}
	h.HandleRequest(req, ctx)

	upstream := &http.Response{
		StatusCode: 404,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte("missing"))),
		Request:    req,
	}
	got := h.HandleResponse(upstream, ctx)
	if got.StatusCode != 200 {
		t.Fatalf("expected fallback status 200, got %d", got.StatusCode)
	}
	if string(readAll(t, got.Body)) != "OLD-INDEX-BODY" {
		t.Fatal("expected cached body fallback")
	}
}

// Cache miss on a revalidate URL: no fallback available. Upstream 5xx is
// served as-is and NOT cached.
func TestRevalidate_Miss_UpstreamErrorPropagates(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	reqURL := "https://pypi.example/simple/urllib3/"
	req := mkReq(t, "GET", reqURL)
	ctx := &goproxy.ProxyCtx{Req: req}
	if _, resp := h.HandleRequest(req, ctx); resp != nil {
		t.Fatal("cache miss should defer to upstream")
	}
	state, ok := ctx.UserData.(*reqState)
	if !ok {
		t.Fatalf("expected reqState, got %T", ctx.UserData)
	}
	if state.fallback != nil {
		t.Fatal("cache miss should leave fallback nil")
	}

	upstream := &http.Response{
		StatusCode: 503,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte("nope"))),
		Request:    req,
	}
	got := h.HandleResponse(upstream, ctx)
	if got.StatusCode != 503 {
		t.Fatalf("expected upstream 503 passed through, got %d", got.StatusCode)
	}

	key := ComputeCacheKey(req, h.keyHeaders)
	if exists, _ := c.Exists(context.Background(), key); exists {
		t.Fatal("503 response must not be cached")
	}
}

// Legacy entry (zero CachedAt) is treated as immediately stale and
// triggers upstream — confirms the "Treat as immediately stale" rollout
// behavior chosen for legacy entries.
func TestRevalidate_LegacyEntry_TreatedAsStale(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	seedCache(t, c, h, "https://pypi.example/simple/urllib3/", "LEGACY-BODY", time.Time{})

	req := mkReq(t, "GET", "https://pypi.example/simple/urllib3/")
	ctx := &goproxy.ProxyCtx{Req: req}
	_, resp := h.HandleRequest(req, ctx)
	if resp != nil {
		t.Fatalf("legacy entry should be treated as stale, got cached response %v", resp)
	}
	state, ok := ctx.UserData.(*reqState)
	if !ok || string(state.fallback) != "LEGACY-BODY" {
		t.Fatalf("legacy body should still be available as fallback, state=%+v", state)
	}
}

// Non-matching URL keeps the existing cache-hit-serves-cached behavior
// regardless of CachedAt age — only revalidate-matching URLs check
// freshness.
func TestRevalidate_NonMatchingURL_UnaffectedByCachedAt(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	// Cached 24h ago, but URL doesn't match /simple/.
	seedCache(t, c, h, "https://example.com/static.tar.gz", "OLD-IMMUTABLE-BODY",
		now.Add(-24*time.Hour))

	req := mkReq(t, "GET", "https://example.com/static.tar.gz")
	ctx := &goproxy.ProxyCtx{Req: req}
	_, resp := h.HandleRequest(req, ctx)
	if resp == nil {
		t.Fatal("non-matching URL should serve cached regardless of age")
	}
	if string(readAll(t, resp.Body)) != "OLD-IMMUTABLE-BODY" {
		t.Fatal("served body did not match cached")
	}
}

// TestHandleRequest_NilURL exercises the case where goproxy's MITM path
// invokes our request handler with req.URL == nil. This happens when
// url.Parse fails inside goproxy.handleHttps (https.go:273); goproxy still
// calls filterRequest before checking the parse error, so our handler must
// not dereference req.URL.
func TestHandleRequest_NilURL(t *testing.T) {
	h := &Handler{
		methods: map[string]bool{},
		mode:    ModeServe,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	req := &http.Request{
		Method: "GET",
		Host:   "example.com",
		Header: http.Header{},
		URL:    nil,
	}
	ctx := &goproxy.ProxyCtx{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("HandleRequest panicked on nil URL: %v", r)
		}
	}()

	gotReq, gotResp := h.HandleRequest(req, ctx)
	if gotReq != req {
		t.Fatalf("returned request: got %p, want %p", gotReq, req)
	}
	if gotResp != nil {
		t.Fatalf("returned response: got %v, want nil (let goproxy hit its err path)", gotResp)
	}
}
