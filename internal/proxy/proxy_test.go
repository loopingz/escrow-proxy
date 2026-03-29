package proxy_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
