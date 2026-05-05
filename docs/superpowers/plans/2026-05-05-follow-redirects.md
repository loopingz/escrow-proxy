# Follow Upstream Redirects Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the proxy follow upstream HTTP redirects server-side and cache the terminal response body under the original URL's cache key, so CI runs replay deterministically without leaking transient signed-redirect URLs.

**Architecture:** A new `redirectFollower` type implements `goproxy.RoundTripper` by composing an `*http.Client` (which natively follows up to 10 redirects with stdlib header-stripping rules). The handler installs it via `ctx.RoundTripper` on each request, so goproxy's upstream call goes through it instead of the default `proxy.Tr.RoundTrip`. `HandleResponse` already caches whatever final response it sees under the original URL's key — no cache-layer changes.

**Tech Stack:** Go 1.25+, `github.com/elazarl/goproxy` v1.8.3, `net/http`, `net/http/httptest`, existing `internal/proxy`, `internal/cache`, `internal/storage` packages.

**Spec:** [`docs/superpowers/specs/2026-05-05-follow-redirects-design.md`](../specs/2026-05-05-follow-redirects-design.md)

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/proxy/transport.go` | Create | Defines `redirectFollower` (goproxy.RoundTripper that follows redirects via http.Client.Do) |
| `internal/proxy/handler.go` | Modify | Adds `upstream goproxy.RoundTripper` field to `Handler`; sets `ctx.RoundTripper = h.upstream` in `HandleRequest` |
| `internal/proxy/proxy.go` | Modify | Constructs the redirectFollower in `New()` and assigns it to `Handler.upstream` |
| `internal/proxy/proxy_test.go` | Modify | Adds 8 new integration tests |
| `README.md` | Modify | Adds one line under "Request Flow" noting that the proxy follows upstream redirects |

---

## Task 1: Wire `redirectFollower` end-to-end with single-redirect test

This task introduces the type, wires it through the handler, and adds the first integration test. After this task lands, all subsequent tasks add tests that should pass without further code changes (they exercise stdlib behavior reaching us through the new seam).

**Files:**
- Create: `internal/proxy/transport.go`
- Modify: `internal/proxy/handler.go` (add field, set `ctx.RoundTripper`)
- Modify: `internal/proxy/proxy.go` (instantiate, pass to handler)
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_FollowsRedirectAndCachesFinalBody`)

- [ ] **Step 1: Write the failing test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_FollowsRedirectAndCachesFinalBody(t *testing.T) {
	var (
		redirectCount int
		finalCount    int
		upstream      *httptest.Server
	)
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			redirectCount++
			http.Redirect(w, r, upstream.URL+"/final", http.StatusFound)
		case "/final":
			finalCount++
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("blob"))
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)

	// Disable client-side redirect-following so we can observe what the
	// proxy itself returned (200 means proxy followed; 302 means it didn't).
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// First request: proxy follows the redirect upstream and caches /redirect.
	resp1, err := client.Get(upstream.URL + "/redirect")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status: got %d, want 200 (proxy did not follow redirect)", resp1.StatusCode)
	}
	if string(body1) != "blob" {
		t.Fatalf("first body: got %q, want %q", body1, "blob")
	}
	if redirectCount != 1 || finalCount != 1 {
		t.Fatalf("upstream calls after first request: got redirect=%d final=%d, want 1/1", redirectCount, finalCount)
	}

	// Second request: served from cache for the ORIGINAL URL — no upstream calls.
	resp2, err := client.Get(upstream.URL + "/redirect")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second request status: got %d, want 200", resp2.StatusCode)
	}
	if string(body2) != "blob" {
		t.Fatalf("cached body: got %q, want %q", body2, "blob")
	}
	if redirectCount != 1 || finalCount != 1 {
		t.Fatalf("upstream calls after cache hit: got redirect=%d final=%d, want 1/1 (cache miss)", redirectCount, finalCount)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy/ -run TestProxy_FollowsRedirectAndCachesFinalBody -v`

Expected: FAIL. The proxy currently passes the 302 through unchanged. With `CheckRedirect = ErrUseLastResponse`, the client receives the 302 directly. The first assertion `resp1.StatusCode != 200` triggers, or the body assertion fails because the body is the redirect's HTML stub, not "blob".

- [ ] **Step 3: Create `internal/proxy/transport.go`**

```go
package proxy

import (
	"net/http"

	"github.com/elazarl/goproxy"
)

// redirectFollower is a goproxy.RoundTripper that follows upstream redirects
// before returning the final response. It composes an *http.Client so that
// net/http's standard redirect handling is reused: relative-URL resolution,
// cross-host header stripping (Authorization, Cookie, WWW-Authenticate), and
// the 10-hop limit.
//
// This intentionally does more than a stdlib http.RoundTripper, which is
// contractually a single round trip with no redirects. escrow-proxy needs the
// terminal body of a redirect chain so the cache key (computed from the
// original URL) maps to the actual content rather than to a transient redirect
// (e.g. a signed CDN URL with a short expiry). goproxy invokes the upstream
// transport through this single seam, which makes it a clean place to absorb
// the redirect chain.
type redirectFollower struct {
	client *http.Client
}

func newRedirectFollower(base http.RoundTripper) *redirectFollower {
	return &redirectFollower{
		client: &http.Client{
			Transport: base,
			// CheckRedirect: nil → stdlib default: follow up to 10 hops.
		},
	}
}

// RoundTrip implements goproxy.RoundTripper. The ctx parameter is unused; the
// redirect chain is fully internal to http.Client.Do.
func (r *redirectFollower) RoundTrip(req *http.Request, _ *goproxy.ProxyCtx) (*http.Response, error) {
	return r.client.Do(req)
}
```

- [ ] **Step 4: Modify `internal/proxy/handler.go` to add upstream field and install it on each request**

Add the import for `goproxy` if not already imported (it is). Update the `Handler` struct and `HandleRequest`:

```go
type Handler struct {
	cache           *cache.Cache
	keyHeaders      []string
	methods         map[string]bool
	excludePatterns []*regexp.Regexp
	mode            Mode
	logger          *slog.Logger
	timeout         time.Duration
	upstream        goproxy.RoundTripper
}
```

In `HandleRequest`, install the round-tripper as the very first action (so it applies even to bypassed requests, preserving today's "let it through" semantics with the additional benefit that redirects are followed for those too):

```go
func (h *Handler) HandleRequest(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if h.upstream != nil {
		ctx.RoundTripper = h.upstream
	}

	if bypass, reason := h.bypass(req); bypass {
		// ... existing body unchanged
	}
	// ... rest unchanged
}
```

The `if h.upstream != nil` guard keeps existing tests that construct `Handler` directly (none today, but defensive) working without refactor.

- [ ] **Step 5: Modify `internal/proxy/proxy.go` to construct and pass the round-tripper**

In `New()`, add `"net/http"` to imports (not yet present in this file). Update the `handler` literal:

```go
import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)
```

```go
	handler := &Handler{
		cache:           cfg.Cache,
		keyHeaders:      cfg.KeyHeaders,
		methods:         methods,
		excludePatterns: cfg.ExcludePatterns,
		mode:            cfg.Mode,
		logger:          cfg.Logger,
		timeout:         cfg.UpstreamTimeout,
		upstream:        newRedirectFollower(http.DefaultTransport),
	}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/proxy/ -run TestProxy_FollowsRedirectAndCachesFinalBody -v`

Expected: PASS.

- [ ] **Step 7: Run the full proxy test suite to verify no regressions**

Run: `go test ./internal/proxy/ -v`

Expected: PASS for all existing tests plus the new one.

- [ ] **Step 8: Commit**

```bash
git add internal/proxy/transport.go internal/proxy/handler.go internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat(proxy): follow upstream redirects, cache terminal body"
```

---

## Task 2: Multi-hop redirect

**Files:**
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_FollowsMultiHopRedirect`)

- [ ] **Step 1: Write the test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_FollowsMultiHopRedirect(t *testing.T) {
	var (
		hop1, hop2, finalCount int
		upstream               *httptest.Server
	)
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			hop1++
			http.Redirect(w, r, upstream.URL+"/b", http.StatusFound)
		case "/b":
			hop2++
			http.Redirect(w, r, upstream.URL+"/final", http.StatusMovedPermanently)
		case "/final":
			finalCount++
			w.Write([]byte("two-hops"))
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client.Get(upstream.URL + "/a")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if string(body) != "two-hops" {
		t.Fatalf("body: got %q, want %q", body, "two-hops")
	}
	if hop1 != 1 || hop2 != 1 || finalCount != 1 {
		t.Fatalf("upstream calls: got hop1=%d hop2=%d final=%d, want 1/1/1", hop1, hop2, finalCount)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestProxy_FollowsMultiHopRedirect -v`

Expected: PASS (stdlib follows up to 10 hops by default).

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "test(proxy): cover multi-hop redirect chains"
```

---

## Task 3: Cross-host redirect

**Files:**
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_FollowsRedirectsAcrossHosts`)

- [ ] **Step 1: Write the test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_FollowsRedirectsAcrossHosts(t *testing.T) {
	finalCount := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalCount++
		w.Write([]byte("cross-host-payload"))
	}))
	defer target.Close()

	originCount := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCount++
		http.Redirect(w, r, target.URL+"/blob", http.StatusFound)
	}))
	defer origin.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// First request: cache key = origin URL
	resp1, err := client.Get(origin.URL + "/pkg")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if string(body1) != "cross-host-payload" {
		t.Fatalf("body: got %q, want %q", body1, "cross-host-payload")
	}
	if originCount != 1 || finalCount != 1 {
		t.Fatalf("first request: origin=%d final=%d, want 1/1", originCount, finalCount)
	}

	// Second request to the origin URL: cache hit, no upstream traffic.
	resp2, err := client.Get(origin.URL + "/pkg")
	if err != nil {
		t.Fatalf("request2: %v", err)
	}
	io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if originCount != 1 || finalCount != 1 {
		t.Fatalf("after cache hit: origin=%d final=%d, want 1/1 (no new calls)", originCount, finalCount)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestProxy_FollowsRedirectsAcrossHosts -v`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "test(proxy): cover cross-host redirect with origin URL as cache key"
```

---

## Task 4: Auth header stripped on cross-host hop

**Files:**
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_StripsAuthHeaderOnCrossHostRedirect`)

- [ ] **Step 1: Write the test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_StripsAuthHeaderOnCrossHostRedirect(t *testing.T) {
	var (
		gotAuthAtTarget string
		gotAuthAtOrigin string
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthAtTarget = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthAtOrigin = r.Header.Get("Authorization")
		http.Redirect(w, r, target.URL+"/x", http.StatusFound)
	}))
	defer origin.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	req, _ := http.NewRequest("GET", origin.URL+"/start", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	if gotAuthAtOrigin != "Bearer s3cret" {
		t.Fatalf("origin Authorization: got %q, want %q (header should reach origin)", gotAuthAtOrigin, "Bearer s3cret")
	}
	if gotAuthAtTarget != "" {
		t.Fatalf("target Authorization: got %q, want empty (stdlib should strip across hosts)", gotAuthAtTarget)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestProxy_StripsAuthHeaderOnCrossHostRedirect -v`

Expected: PASS. (`net/http`'s default `http.Client.redirectBehavior` strips `Authorization`, `Cookie`, and `WWW-Authenticate` when redirecting to a different host.)

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "test(proxy): verify Authorization stripped on cross-host redirect"
```

---

## Task 5: Too-many-redirects returns 502

**Files:**
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_TooManyRedirectsReturns502`)

- [ ] **Step 1: Write the test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_TooManyRedirectsReturns502(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Self-loop: every path redirects to /loop.
		http.Redirect(w, r, upstream.URL+"/loop", http.StatusFound)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client.Get(upstream.URL + "/start")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", resp.StatusCode)
	}

	// Cache must remain empty: no entry was written for /start.
	_, _, err = c.Get(t.Context(), proxy.ComputeCacheKey(mustReq(t, "GET", upstream.URL+"/start"), nil))
	if err == nil {
		t.Fatalf("cache should be empty for failed redirect chain, but got an entry")
	}
}

// mustReq is a helper to build an *http.Request for cache-key computation in tests.
func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}
```

Note: this task introduces a `mustReq` helper. If `proxy_test.go` already has a similar helper, reuse it instead. Verify with `grep -n "func mustReq\|func makeReq\|func newReq" internal/proxy/proxy_test.go` before adding.

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestProxy_TooManyRedirectsReturns502 -v`

Expected: PASS. (`http.Client` returns "stopped after 10 redirects"; goproxy renders RoundTrip errors as 502 to the client.)

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "test(proxy): redirect loop returns 502 and is not cached"
```

---

## Task 6: Chain ending in 404 is not cached

**Files:**
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_RedirectChainEndingIn404NotCached`)

- [ ] **Step 1: Write the test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_RedirectChainEndingIn404NotCached(t *testing.T) {
	var (
		redirectCount, missingCount int
		upstream                    *httptest.Server
	)
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			redirectCount++
			http.Redirect(w, r, upstream.URL+"/missing", http.StatusFound)
		case "/missing":
			missingCount++
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found"))
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	for i := 0; i < 2; i++ {
		resp, err := client.Get(upstream.URL + "/start")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("request %d status: got %d, want 404", i, resp.StatusCode)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	// Both runs should hit upstream (4xx is not cached, even at chain end).
	if redirectCount != 2 || missingCount != 2 {
		t.Fatalf("upstream calls: got redirect=%d missing=%d, want 2/2 (4xx not cached)", redirectCount, missingCount)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestProxy_RedirectChainEndingIn404NotCached -v`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "test(proxy): redirect chain ending in 404 is not cached"
```

---

## Task 7: Offline mode serves cached final body (end-to-end CI replay)

This is the integration test that proves the fix delivers the user-visible benefit: a recorded CI run replays cleanly in offline mode without needing the original signed redirect URL to still be valid.

**Files:**
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_OfflineMode_ServesCachedFinalBodyForRedirect`)

- [ ] **Step 1: Write the test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_OfflineMode_ServesCachedFinalBodyForRedirect(t *testing.T) {
	var (
		redirectCount, finalCount int
		upstream                  *httptest.Server
	)
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pkg":
			redirectCount++
			http.Redirect(w, r, upstream.URL+"/blob", http.StatusFound)
		case "/blob":
			finalCount++
			w.Write([]byte("recorded-blob"))
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
	}))

	dir := t.TempDir()

	// --- Phase 1: serve mode populates the cache via a redirect chain ---
	store1 := storage.NewLocal(dir)
	c1 := cache.New(store1)
	_, client1 := setupProxy(t, proxy.ModeServe, c1)
	client1.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client1.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if string(mustReadAll(t, resp.Body)) != "recorded-blob" {
		t.Fatalf("populate body wrong")
	}
	resp.Body.Close()

	if redirectCount != 1 || finalCount != 1 {
		t.Fatalf("populate phase: got redirect=%d final=%d, want 1/1", redirectCount, finalCount)
	}

	// --- Phase 2: shut down upstream and verify offline mode serves the original URL ---
	upstreamURL := upstream.URL
	upstream.Close()

	store2 := storage.NewLocal(dir)
	c2 := cache.New(store2)
	_, client2 := setupProxy(t, proxy.ModeOffline, c2)
	client2.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp2, err := client2.Get(upstreamURL + "/pkg")
	if err != nil {
		t.Fatalf("offline request: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("offline status: got %d, want 200", resp2.StatusCode)
	}
	if string(body) != "recorded-blob" {
		t.Fatalf("offline body: got %q, want %q", body, "recorded-blob")
	}
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return b
}
```

If `mustReadAll` already exists, drop the helper. Verify with `grep -n "func mustReadAll" internal/proxy/proxy_test.go`.

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestProxy_OfflineMode_ServesCachedFinalBodyForRedirect -v`

Expected: PASS. The offline phase serves the original `/pkg` URL straight from the cache (the upstream is closed, so any cache miss would fail).

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "test(proxy): offline replay of redirect-chain cache works end-to-end"
```

---

## Task 8: Relative Location header

**Files:**
- Test: `internal/proxy/proxy_test.go` (new test `TestProxy_RelativeLocationRedirect`)

- [ ] **Step 1: Write the test**

Append to `internal/proxy/proxy_test.go`:

```go
func TestProxy_RelativeLocationRedirect(t *testing.T) {
	var (
		startCount, elsewhereCount int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			startCount++
			// Relative path — stdlib should resolve against the request URL.
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
		case "/elsewhere":
			elsewhereCount++
			w.Write([]byte("relative-ok"))
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client.Get(upstream.URL + "/start")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if string(body) != "relative-ok" {
		t.Fatalf("body: got %q, want %q", body, "relative-ok")
	}
	if startCount != 1 || elsewhereCount != 1 {
		t.Fatalf("upstream calls: got start=%d elsewhere=%d, want 1/1", startCount, elsewhereCount)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestProxy_RelativeLocationRedirect -v`

Expected: PASS.

- [ ] **Step 3: Run the full test suite for the project**

Run: `go test ./...`

Expected: PASS across all packages.

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "test(proxy): relative Location header is resolved by redirect follower"
```

---

## Task 9: Document the behavior in README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Find the "Request Flow" section in `README.md`**

Read `README.md` lines 260–272 (the existing fenced block titled `## Request Flow`).

- [ ] **Step 2: Add a single line under the existing flow**

Replace this block:

```
Client Request
  → CONNECT → MITM TLS Intercept
  → Compute cache key: SHA256(method + url + headers + body_hash)
  → Check L1 (local) → hit? → return cached response
  → Check L2 (GCS/S3) → hit? → backfill L1 + return cached response
  → Miss → forward upstream (with timeout)
  → Cache response (2xx-3xx only) to all tiers
  → Return response to client
```

With:

```
Client Request
  → CONNECT → MITM TLS Intercept
  → Compute cache key: SHA256(method + url + headers + body_hash)
  → Check L1 (local) → hit? → return cached response
  → Check L2 (GCS/S3) → hit? → backfill L1 + return cached response
  → Miss → forward upstream (follows redirects up to 10 hops)
  → Cache terminal response (2xx only) under the original URL
  → Return response to client
```

(Two changes: "follows redirects up to 10 hops" added on the forward line, and "terminal response (2xx only) under the original URL" replaces "response (2xx-3xx only) to all tiers" — note that 3xx is no longer cached because we never see a 3xx as the terminal response after redirect-following.)

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: note that proxy follows upstream redirects"
```

---

## Self-Review Notes

**Spec coverage:**
- Architecture (`redirectFollower` + `ctx.RoundTripper`) → Task 1.
- Data flow (cache key on original URL, terminal body cached) → Tasks 1, 7.
- Error handling, all 9 rows of the matrix:
  - Chain >10 hops → Task 5.
  - Loop → Task 5 (same path as 10-hop overflow).
  - Unreachable / DNS / TLS error → Implicit; goproxy's existing 502 path covers it. The 10-hop test exercises the same error → 502 mechanism.
  - 4xx/5xx terminal → Task 6.
  - Relative `Location` → Task 8.
  - Malformed `Location` → Same path as too-many-redirects (error from `http.Client.Do` → 502). Not separately tested; would require a synthetic upstream that emits a malformed URL, which is awkward to set up in `httptest`.
  - Cross-host header stripping → Task 4.
  - `UpstreamTimeout` mid-chain → Out of scope per the spec's Non-Goals.
  - Client cancellation → Behavior comes from `req.Context()` in `client.Do`. Not separately tested; same plumbing as existing handler tests.
  - 304 → No-op preservation; not separately tested.
- Testing matrix (8 tests) → Tasks 1–8.
- Files Changed list → All four files plus README hit by Tasks 1, 9.

**Placeholder scan:** No "TBD"/"TODO"/"add error handling"/"similar to" placeholders. Every code step has the actual code.

**Type/signature consistency:**
- `redirectFollower.RoundTrip` signature `(req *http.Request, _ *goproxy.ProxyCtx) (*http.Response, error)` matches goproxy's `RoundTripper` interface in v1.8.3.
- `Handler.upstream goproxy.RoundTripper` field type matches what `ctx.RoundTripper` accepts.
- `newRedirectFollower(http.RoundTripper)` constructor signature matches its single call site in `proxy.go`.
- Helper functions `mustReq` and `mustReadAll` are introduced once in Tasks 5 and 7 respectively, with `grep` checks to avoid duplicate definitions.
