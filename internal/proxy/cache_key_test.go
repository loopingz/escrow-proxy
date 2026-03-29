package proxy_test

import (
	"net/http"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/proxy"
)

func TestComputeCacheKey_Deterministic(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req1.Header.Set("Accept", "application/json")

	req2, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2.Header.Set("Accept", "application/json")

	headers := []string{"Accept"}
	k1 := proxy.ComputeCacheKey(req1, headers)
	k2 := proxy.ComputeCacheKey(req2, headers)

	if k1 != k2 {
		t.Fatalf("same request produced different keys: %s vs %s", k1, k2)
	}
}

func TestComputeCacheKey_DifferentURLsDiffer(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2, _ := http.NewRequest("GET", "https://example.com/bar", nil)

	k1 := proxy.ComputeCacheKey(req1, nil)
	k2 := proxy.ComputeCacheKey(req2, nil)

	if k1 == k2 {
		t.Fatal("different URLs should produce different keys")
	}
}

func TestComputeCacheKey_DifferentMethodsDiffer(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2, _ := http.NewRequest("POST", "https://example.com/foo", nil)

	k1 := proxy.ComputeCacheKey(req1, nil)
	k2 := proxy.ComputeCacheKey(req2, nil)

	if k1 == k2 {
		t.Fatal("different methods should produce different keys")
	}
}

func TestComputeCacheKey_HeaderOrderIrrelevant(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req1.Header.Set("Accept", "text/html")
	req1.Header.Set("Accept-Encoding", "gzip")

	req2, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	req2.Header.Set("Accept", "text/html")

	headers := []string{"Accept-Encoding", "Accept"}
	k1 := proxy.ComputeCacheKey(req1, headers)
	k2 := proxy.ComputeCacheKey(req2, headers)

	if k1 != k2 {
		t.Fatalf("header order should not matter: %s vs %s", k1, k2)
	}
}

func TestComputeCacheKey_IgnoresNonSelectedHeaders(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req1.Header.Set("Accept", "text/html")
	req1.Header.Set("User-Agent", "curl")

	req2, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2.Header.Set("Accept", "text/html")
	req2.Header.Set("User-Agent", "wget")

	headers := []string{"Accept"}
	k1 := proxy.ComputeCacheKey(req1, headers)
	k2 := proxy.ComputeCacheKey(req2, headers)

	if k1 != k2 {
		t.Fatal("non-selected headers should not affect key")
	}
}
