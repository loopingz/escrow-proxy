package proxy_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/proxy"
	"github.com/loopingz/escrow-proxy/internal/storage"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

// setupDigestProxy builds a proxy with digest verification at the given
// action. Returns (client, cache, upstream-URL-base) — caller is
// responsible for closing the upstream server they create.
func setupDigestProxy(t *testing.T, action proxy.DigestMismatchAction, c *cache.Cache) *http.Client {
	t.Helper()
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	p := proxy.New(&proxy.Config{
		Mode:                 proxy.ModeServe,
		Cache:                c,
		CertCache:            tlspkg.NewCertCache(ca, 100),
		CA:                   ca,
		KeyHeaders:           []string{},
		Logger:               testLogger(),
		VerifyDigest:         true,
		DigestMismatchAction: action,
	})
	proxyServer := httptest.NewServer(p)
	t.Cleanup(proxyServer.Close)

	proxyURL, _ := url.Parse(proxyServer.URL)
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestVerifyDigest_MatchCachesNormally(t *testing.T) {
	body := []byte("hello-blob")
	digest := sha256Hex(body)
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	client := setupDigestProxy(t, proxy.DigestMismatchError, c)

	urlStr := upstream.URL + "/v2/library/foo/blobs/sha256:" + digest

	// First request: cache miss, upstream verified, cached.
	resp, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != string(body) {
		t.Fatalf("first body: got %q, want %q", got, body)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status: %d, want 200", resp.StatusCode)
	}

	// Second request: should be a cache hit (upstream not called again).
	resp2, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	got2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(got2) != string(body) {
		t.Fatalf("cached body: got %q, want %q", got2, body)
	}
	if callCount != 1 {
		t.Fatalf("upstream called %d times, want 1 (second should be cache hit)", callCount)
	}
}

func TestVerifyDigest_MismatchErrorReturns502(t *testing.T) {
	body := []byte("actual-content")
	// Claim a different digest in the URL than what the body hashes to.
	wrongDigest := sha256Hex([]byte("different-content"))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	client := setupDigestProxy(t, proxy.DigestMismatchError, c)

	urlStr := upstream.URL + "/v2/library/foo/blobs/sha256:" + wrongDigest
	resp, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", resp.StatusCode)
	}

	// Cache must not have stored the mismatched response. Hit upstream
	// again with the same bogus URL — it should still 502, not serve a
	// stale cached body.
	resp2, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadGateway {
		t.Fatalf("second status: got %d, want 502 (cache must not be poisoned)", resp2.StatusCode)
	}
}

func TestVerifyDigest_MismatchPassthroughServesButDoesNotCache(t *testing.T) {
	body := []byte("actual-content")
	wrongDigest := sha256Hex([]byte("different-content"))
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	client := setupDigestProxy(t, proxy.DigestMismatchPassthrough, c)

	urlStr := upstream.URL + "/v2/library/foo/blobs/sha256:" + wrongDigest

	// First request: mismatch, passthrough — client receives the body,
	// but proxy does not cache it.
	resp, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (passthrough)", resp.StatusCode)
	}
	if string(got) != string(body) {
		t.Fatalf("body: got %q, want %q", got, body)
	}

	// Second request: cache should still be empty -> upstream hit again.
	resp2, _ := client.Get(urlStr)
	resp2.Body.Close()
	if callCount != 2 {
		t.Fatalf("upstream called %d times, want 2 (passthrough must not cache)", callCount)
	}
}

func TestVerifyDigest_NonDigestURLNotChecked(t *testing.T) {
	// A URL that does not encode a content digest should never be
	// verified, regardless of body bytes. This protects normal tag-ref
	// and non-OCI traffic from false positives.
	body := []byte("anything")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	client := setupDigestProxy(t, proxy.DigestMismatchError, c)

	resp, err := client.Get(upstream.URL + "/v2/library/foo/manifests/1.0")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (non-digest URL must pass through unverified)", resp.StatusCode)
	}
}

func TestVerifyDigest_PoisonedCacheHitEvictsAndRefetches(t *testing.T) {
	// Seed the cache with a body that does NOT match the URL digest,
	// simulating a previously-poisoned cache entry. The proxy must
	// detect the mismatch on cache hit, evict, refetch from upstream,
	// and serve the fresh correct body.
	correctBody := []byte("real-content")
	digest := sha256Hex(correctBody)
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		w.Write(correctBody)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	client := setupDigestProxy(t, proxy.DigestMismatchError, c)

	urlStr := upstream.URL + "/v2/library/foo/blobs/sha256:" + digest

	// First request: nothing cached, fetches upstream, body matches digest,
	// gets cached normally.
	resp, _ := client.Get(urlStr)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if upstreamCalls != 1 {
		t.Fatalf("after first GET, upstream called %d times, want 1", upstreamCalls)
	}

	// Poison the cache: overwrite the stored body with garbage that no
	// longer matches the digest. We use cache.Put directly with the same
	// computed key the proxy would have used.
	key := poisonCacheEntry(t, c, "GET", urlStr, []byte("poisoned-bytes"))

	// Second request: cache hit reads the poisoned body, digest verifies
	// against URL fail, proxy evicts the bad entry, falls through to
	// upstream, serves the correct body. Upstream should be called once
	// more (total 2).
	resp2, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	got, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(got) != string(correctBody) {
		t.Fatalf("body after eviction: got %q, want %q", got, correctBody)
	}
	if upstreamCalls != 2 {
		t.Fatalf("after second GET, upstream called %d times, want 2 (poisoned hit must trigger refetch)", upstreamCalls)
	}

	// Confirm the now-evicted-then-refetched entry is once again a clean
	// cache hit on the third request (upstream not called a third time).
	resp3, _ := client.Get(urlStr)
	got3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if string(got3) != string(correctBody) {
		t.Fatalf("third body: got %q, want %q", got3, correctBody)
	}
	if upstreamCalls != 2 {
		t.Fatalf("third GET: upstream called %d times, want still 2 (should be cache hit)", upstreamCalls)
	}
	_ = key
}

// poisonCacheEntry writes garbage into the cache under the same key the
// proxy would compute for (method, url, no body), and returns that key.
// The cache key formula is mirrored from cache_key.go; if it diverges,
// this helper needs to be updated.
func poisonCacheEntry(t *testing.T, c *cache.Cache, method, urlStr string, body []byte) string {
	t.Helper()
	u, err := url.Parse(urlStr)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	req := &http.Request{
		Method: method,
		URL:    u,
		Header: http.Header{},
	}
	// Match the proxy's setupDigestProxy KeyHeaders == nil/empty.
	key := proxy.ComputeCacheKey(req, nil)
	meta := &cache.EntryMeta{
		Method:     method,
		URL:        urlStr,
		StatusCode: http.StatusOK,
		Header:     http.Header{},
	}
	if err := c.Put(req.Context(), key, meta, bytes.NewReader(body)); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}
	return key
}
