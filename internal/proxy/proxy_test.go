package proxy_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/proxy"
	"github.com/loopingz/escrow-proxy/internal/storage"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func setupProxy(t *testing.T, mode proxy.Mode, c *cache.Cache) (*httptest.Server, *http.Client) {
	t.Helper()
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certCache := tlspkg.NewCertCache(ca, 100)

	p := proxy.New(&proxy.Config{
		Mode:       mode,
		Cache:      c,
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{},
		Logger:     testLogger(),
	})

	proxyServer := httptest.NewServer(p)
	t.Cleanup(proxyServer.Close)

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	return proxyServer, client
}

func TestProxy_ServeMode_CachesResponse(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("package-data"))
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)

	// First request -- hits upstream
	resp, err := client.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "package-data" {
		t.Fatalf("body: got %q, want %q", body, "package-data")
	}
	if callCount != 1 {
		t.Fatalf("expected 1 upstream call, got %d", callCount)
	}

	// Second request -- should be served from cache
	resp2, err := client.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body2) != "package-data" {
		t.Fatalf("cached body: got %q, want %q", body2, "package-data")
	}
	if callCount != 1 {
		t.Fatalf("expected still 1 upstream call (cached), got %d", callCount)
	}
}

func TestProxy_ServeMode_KeyHeadersDifferentiate(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte("response-" + r.Header.Get("Accept")))
	}))
	defer upstream.Close()

	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certCache := tlspkg.NewCertCache(ca, 100)
	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)

	p := proxy.New(&proxy.Config{
		Mode:       proxy.ModeServe,
		Cache:      c,
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{"Accept"},
		Logger:     testLogger(),
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	// Request with Accept: text/plain
	req1, _ := http.NewRequest("GET", upstream.URL+"/pkg", nil)
	req1.Header.Set("Accept", "text/plain")
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("request 1: %v", err)
	}
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	// Request with Accept: application/json -- different cache key
	req2, _ := http.NewRequest("GET", upstream.URL+"/pkg", nil)
	req2.Header.Set("Accept", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("request 2: %v", err)
	}
	io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if callCount != 2 {
		t.Fatalf("expected 2 upstream calls (different Accept), got %d", callCount)
	}
}

func TestProxy_OfflineMode_Returns502OnMiss(t *testing.T) {
	store := storage.NewLocal(t.TempDir()) // empty cache
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeOffline, c)

	resp, err := client.Get("http://example.com/missing")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

func TestProxy_OfflineMode_ServesCachedEntry(t *testing.T) {
	// Pre-populate cache via serve mode
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("cached-content"))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	store := storage.NewLocal(dir)
	c := cache.New(store)

	// Use serve mode to populate cache
	_, client := setupProxy(t, proxy.ModeServe, c)

	resp, err := client.Get(upstream.URL + "/item")
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	// Now create an offline proxy with the same cache
	store2 := storage.NewLocal(dir)
	c2 := cache.New(store2)
	_, client2 := setupProxy(t, proxy.ModeOffline, c2)

	resp2, err := client2.Get(upstream.URL + "/item")
	if err != nil {
		t.Fatalf("offline request: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body) != "cached-content" {
		t.Fatalf("offline body: got %q, want %q", body, "cached-content")
	}
}

func TestProxy_RecordAndOffline_RoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("recorded-data"))
	}))
	defer upstream.Close()

	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certCache := tlspkg.NewCertCache(ca, 100)
	dir := t.TempDir()

	// --- Record phase ---
	store := storage.NewLocal(filepath.Join(dir, "cache"))
	c := cache.New(store)

	archivePath := filepath.Join(dir, "archive.tar.gz")
	format := &archive.TarGzFormat{}
	archiveWriter, err := format.NewWriter(archivePath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	recorder := cache.NewRecorder(c, archiveWriter)

	p := proxy.New(&proxy.Config{
		Mode:       proxy.ModeRecord,
		Cache:      recorder.Cache(),
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{},
		Logger:     testLogger(),
	})

	proxyServer := httptest.NewServer(p)

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	// Make request through recording proxy
	resp, err := client.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("record request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "recorded-data" {
		t.Fatalf("record body: got %q", body)
	}

	proxyServer.Close()

	// Finalize archive
	if err := recorder.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// --- Offline phase ---
	archiveReader, err := format.NewReader(archivePath)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer archiveReader.Close()

	archiveStore := cache.NewArchiveStorage(archiveReader)
	offlineCache := cache.New(archiveStore)

	p2 := proxy.New(&proxy.Config{
		Mode:       proxy.ModeOffline,
		Cache:      offlineCache,
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{},
		Logger:     testLogger(),
	})

	proxyServer2 := httptest.NewServer(p2)
	defer proxyServer2.Close()

	proxyURL2, _ := url.Parse(proxyServer2.URL)
	client2 := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL2),
		},
	}

	// Request should be served from archive
	resp2, err := client2.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("offline request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body2) != "recorded-data" {
		t.Fatalf("offline body: got %q, want %q", body2, "recorded-data")
	}
}

func TestProxy_DoesNotCache5xx(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)

	// Two requests -- both should hit upstream (5xx not cached)
	resp1, err := client.Get(upstream.URL + "/fail")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	resp2, err := client.Get(upstream.URL + "/fail")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if callCount != 2 {
		t.Fatalf("expected 2 upstream calls (5xx not cached), got %d", callCount)
	}
}

func TestProxy_DoesNotCache4xx(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)

	resp1, err := client.Get(upstream.URL + "/missing")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	resp2, err := client.Get(upstream.URL + "/missing")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if callCount != 2 {
		t.Fatalf("expected 2 upstream calls (4xx not cached), got %d", callCount)
	}
}

func TestProxy_BypassesNonAllowedMethods(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte("posted"))
	}))
	defer upstream.Close()

	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certCache := tlspkg.NewCertCache(ca, 100)
	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)

	p := proxy.New(&proxy.Config{
		Mode:       proxy.ModeServe,
		Cache:      c,
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{},
		Methods:    []string{"GET", "HEAD"},
		Logger:     testLogger(),
	})
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	// Two POSTs — both must hit upstream (not cached)
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("POST", upstream.URL+"/api", strings.NewReader("body"))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if callCount != 2 {
		t.Fatalf("expected 2 upstream POST calls (not cached), got %d", callCount)
	}
}

func TestProxy_BypassesExcludedURL(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte("token"))
	}))
	defer upstream.Close()

	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certCache := tlspkg.NewCertCache(ca, 100)
	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)

	p := proxy.New(&proxy.Config{
		Mode:            proxy.ModeServe,
		Cache:           c,
		CertCache:       certCache,
		CA:              ca,
		KeyHeaders:      []string{},
		Methods:         []string{"GET", "HEAD"},
		ExcludePatterns: []*regexp.Regexp{regexp.MustCompile(`/token$`)},
		Logger:          testLogger(),
	})
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	for i := 0; i < 2; i++ {
		resp, err := client.Get(upstream.URL + "/token")
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if callCount != 2 {
		t.Fatalf("expected 2 upstream calls (excluded URL), got %d", callCount)
	}
}

func TestProxy_OfflineMode_BypassedMethodReturns502(t *testing.T) {
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certCache := tlspkg.NewCertCache(ca, 100)
	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)

	p := proxy.New(&proxy.Config{
		Mode:       proxy.ModeOffline,
		Cache:      c,
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{},
		Methods:    []string{"GET", "HEAD"},
		Logger:     testLogger(),
	})
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	req, _ := http.NewRequest("POST", "http://example.com/api", strings.NewReader("b"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for bypassed method in offline, got %d", resp.StatusCode)
	}
}

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

	// Rewrite target.URL host (127.0.0.1) to localhost so stdlib sees a
	// different hostname string and triggers cross-host header stripping.
	// Both still resolve to the same loopback IP.
	targetViaLocalhost := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthAtOrigin = r.Header.Get("Authorization")
		http.Redirect(w, r, targetViaLocalhost+"/x", http.StatusFound)
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

func TestProxy_TooManyRedirectsReturns500(t *testing.T) {
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

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", resp.StatusCode)
	}

	// Cache must remain empty: no entry was written for /start.
	_, _, err = c.Get(t.Context(), proxy.ComputeCacheKey(mustReq(t, "GET", upstream.URL+"/start"), nil))
	if err == nil {
		t.Fatalf("cache should be empty for failed redirect chain, but got an entry")
	}
}

// mustReq builds an *http.Request for cache-key computation in tests.
func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

func TestProxy_PreservesResponseHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "test-value")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	_, client := setupProxy(t, proxy.ModeServe, c)

	// First request populates cache
	resp, err := client.Get(upstream.URL + "/api")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	// Second request from cache -- check headers are preserved
	resp2, err := client.Get(upstream.URL + "/api")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if got := resp2.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type: got %q, want %q", got, "application/json")
	}
	if got := resp2.Header.Get("X-Custom"); got != "test-value" {
		t.Fatalf("X-Custom: got %q, want %q", got, "test-value")
	}
}
