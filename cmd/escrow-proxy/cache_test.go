package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// newTestCache returns a Cache backed by a fresh tempdir local store
// and a helper to seed entries.
func newTestCache(t *testing.T) (*cache.Cache, storage.Storage, func(key, method, url string)) {
	t.Helper()
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
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
	return c, s, put
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
	c, _, _ := newTestCache(t)
	_, _, err := runOpts(c, nil)
	if err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("expected 'exactly one of' error, got %v", err)
	}
}

func TestInvalidate_MultipleFilters(t *testing.T) {
	c, _, _ := newTestCache(t)
	_, _, err := runOpts(c, func(o *invalidateOptions) {
		o.Key = "abc"
		o.All = true
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestInvalidate_AllWithMethodRejected(t *testing.T) {
	c, _, _ := newTestCache(t)
	_, _, err := runOpts(c, func(o *invalidateOptions) {
		o.All = true
		o.Method = "GET"
	})
	if err == nil || !strings.Contains(err.Error(), "--method cannot be combined with --all") {
		t.Fatalf("expected method+all error, got %v", err)
	}
}

// silence unused-import warnings in early tasks
var _ = io.Discard
