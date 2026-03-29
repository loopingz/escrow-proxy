package cache_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestCache_PutAndGet(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	meta := &cache.EntryMeta{
		Method:     "GET",
		URL:        "https://example.com/pkg",
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/octet-stream"}},
	}
	body := []byte("package-contents")

	key := "abc123"
	if err := c.Put(ctx, key, meta, bytes.NewReader(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	gotMeta, gotBody, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer gotBody.Close()

	if gotMeta.URL != meta.URL {
		t.Fatalf("URL: got %s, want %s", gotMeta.URL, meta.URL)
	}
	if gotMeta.StatusCode != 200 {
		t.Fatalf("StatusCode: got %d, want 200", gotMeta.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(gotBody)
	if !bytes.Equal(bodyBytes, body) {
		t.Fatalf("body: got %q, want %q", bodyBytes, body)
	}
}

func TestCache_GetMiss(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	_, _, err := c.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error on cache miss")
	}
}

func TestCache_Exists(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	exists, _ := c.Exists(ctx, "missing")
	if exists {
		t.Fatal("expected false")
	}

	meta := &cache.EntryMeta{Method: "GET", URL: "https://example.com", StatusCode: 200}
	c.Put(ctx, "present", meta, bytes.NewReader([]byte("data")))

	exists, _ = c.Exists(ctx, "present")
	if !exists {
		t.Fatal("expected true")
	}
}
