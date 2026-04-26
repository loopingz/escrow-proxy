package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// newTestCache returns a Cache backed by a fresh tempdir local store
// and a helper to seed entries.
func newTestCache(t *testing.T) (*cache.Cache, func(key, method, url string)) {
	t.Helper()
	c := cache.New(storage.NewLocal(t.TempDir()))
	put := func(key, method, url string) {
		t.Helper()
		meta := &cache.EntryMeta{
			Method:     method,
			URL:        url,
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
		}
		if err := c.Put(context.Background(), key, meta, bytes.NewReader([]byte("body-"+key))); err != nil {
			t.Fatalf("seed Put(%s): %v", key, err)
		}
	}
	return c, put
}

func runOpts(c *cache.Cache, mutate func(*invalidateOptions)) (string, string, error) {
	var stdout, stderr bytes.Buffer
	opts := invalidateOptions{
		Cache:  c,
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if mutate != nil {
		mutate(&opts)
	}
	err := runInvalidate(context.Background(), opts)
	return stdout.String(), stderr.String(), err
}

func TestInvalidate_ZeroFilters(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runOpts(c, nil)
	if err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("expected 'exactly one of' error, got %v", err)
	}
}

func TestInvalidate_MultipleFilters(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runOpts(c, func(o *invalidateOptions) {
		o.Key = "abc"
		o.All = true
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestInvalidate_AllWithMethodRejected(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runOpts(c, func(o *invalidateOptions) {
		o.All = true
		o.Method = "GET"
	})
	if err == nil || !strings.Contains(err.Error(), "--method cannot be combined with --all") {
		t.Fatalf("expected method+all error, got %v", err)
	}
}


func TestInvalidate_Key_DeletesSingleEntry(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://example.com/b")

	stdout, stderr, err := runOpts(c, func(o *invalidateOptions) { o.Key = "k1" })
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}

	exists, _ := c.Exists(context.Background(), "k1")
	if exists {
		t.Fatal("k1 should be gone")
	}
	exists, _ = c.Exists(context.Background(), "k2")
	if !exists {
		t.Fatal("k2 should remain")
	}

	if !strings.Contains(stdout, "k1") {
		t.Fatalf("expected stdout to mention k1, got %q", stdout)
	}
	if !strings.Contains(stdout, "https://example.com/a") {
		t.Fatalf("expected stdout to mention url, got %q", stdout)
	}
	if !strings.Contains(stderr, "deleted 1 entries") {
		t.Fatalf("expected stderr summary, got %q", stderr)
	}
}

func TestInvalidate_Key_MissingReturnsError(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runOpts(c, func(o *invalidateOptions) { o.Key = "ghost" })
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound wrap, got %v", err)
	}
}

func TestInvalidate_URL_DeletesAllVariations(t *testing.T) {
	c, put := newTestCache(t)
	put("get", "GET", "https://example.com/x")
	put("post", "POST", "https://example.com/x")
	put("other", "GET", "https://example.com/y")

	stdout, stderr, err := runOpts(c, func(o *invalidateOptions) {
		o.URL = "https://example.com/x"
	})
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}

	for _, k := range []string{"get", "post"} {
		exists, _ := c.Exists(context.Background(), k)
		if exists {
			t.Fatalf("%s should be gone", k)
		}
	}
	exists, _ := c.Exists(context.Background(), "other")
	if !exists {
		t.Fatal("other should remain")
	}

	lines := strings.Count(strings.TrimSpace(stdout), "\n") + 1
	if lines != 2 {
		t.Fatalf("expected 2 stdout lines, got %d: %q", lines, stdout)
	}
	if !strings.Contains(stderr, "deleted 2 entries") {
		t.Fatalf("expected 'deleted 2 entries', got %q", stderr)
	}
}

func TestInvalidate_URL_WithMethodNarrows(t *testing.T) {
	c, put := newTestCache(t)
	put("get", "GET", "https://example.com/x")
	put("post", "POST", "https://example.com/x")

	_, stderr, err := runOpts(c, func(o *invalidateOptions) {
		o.URL = "https://example.com/x"
		o.Method = "GET"
	})
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}

	exists, _ := c.Exists(context.Background(), "get")
	if exists {
		t.Fatal("get should be gone")
	}
	exists, _ = c.Exists(context.Background(), "post")
	if !exists {
		t.Fatal("post should remain")
	}

	if !strings.Contains(stderr, "deleted 1 entries") {
		t.Fatalf("expected 'deleted 1 entries', got %q", stderr)
	}
}

func TestInvalidate_URL_NoMatches(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/x")

	_, stderr, err := runOpts(c, func(o *invalidateOptions) {
		o.URL = "https://nowhere.test/"
	})
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}
	if !strings.Contains(stderr, "deleted 0 entries") {
		t.Fatalf("expected 'deleted 0 entries', got %q", stderr)
	}
}

func TestInvalidate_URLPrefix_DeletesMatching(t *testing.T) {
	c, put := newTestCache(t)
	put("a", "GET", "https://npmjs.org/pkg/foo")
	put("b", "GET", "https://npmjs.org/pkg/bar")
	put("c", "GET", "https://pypi.org/simple/baz")

	_, stderr, err := runOpts(c, func(o *invalidateOptions) {
		o.URLPrefix = "https://npmjs.org/"
	})
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}

	for _, k := range []string{"a", "b"} {
		exists, _ := c.Exists(context.Background(), k)
		if exists {
			t.Fatalf("%s should be gone", k)
		}
	}
	exists, _ := c.Exists(context.Background(), "c")
	if !exists {
		t.Fatal("c should remain")
	}
	if !strings.Contains(stderr, "deleted 2 entries") {
		t.Fatalf("expected 'deleted 2 entries', got %q", stderr)
	}
}

func TestInvalidate_URLPrefix_WithMethodNarrows(t *testing.T) {
	c, put := newTestCache(t)
	put("get1", "GET", "https://npmjs.org/pkg/foo")
	put("post1", "POST", "https://npmjs.org/pkg/foo")
	put("get2", "GET", "https://npmjs.org/pkg/bar")

	_, stderr, err := runOpts(c, func(o *invalidateOptions) {
		o.URLPrefix = "https://npmjs.org/"
		o.Method = "GET"
	})
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}
	exists, _ := c.Exists(context.Background(), "post1")
	if !exists {
		t.Fatal("post1 should remain")
	}
	if !strings.Contains(stderr, "deleted 2 entries") {
		t.Fatalf("expected 'deleted 2 entries', got %q", stderr)
	}
}
