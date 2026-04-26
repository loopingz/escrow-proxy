package cache_test

import (
	"bytes"
	"context"
	"errors"
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

func TestCache_Delete_RemovesMetaAndBody(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	meta := &cache.EntryMeta{Method: "GET", URL: "https://example.com/x", StatusCode: 200}
	if err := c.Put(ctx, "k1", meta, bytes.NewReader([]byte("body"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := c.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, _ := c.Exists(ctx, "k1")
	if exists {
		t.Fatal("expected entry gone after Delete")
	}
	bodyExists, _ := s.Exists(ctx, "k1.body")
	if bodyExists {
		t.Fatal("expected body gone after Delete")
	}
}

func TestCache_Delete_MissingKeyReturnsErrNotFound(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)

	err := c.Delete(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCache_Delete_BodyPresentMetaAbsent_ReturnsErrNotFound(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	// Inject only the body; meta is the source of truth for existence.
	if err := s.Put(ctx, "k1.body", bytes.NewReader([]byte("orphan"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := c.Delete(ctx, "k1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCache_Walk_VisitsEveryEntryOnce(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	put := func(key, url string) {
		t.Helper()
		meta := &cache.EntryMeta{Method: "GET", URL: url, StatusCode: 200}
		if err := c.Put(ctx, key, meta, bytes.NewReader([]byte("b"))); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	put("k1", "https://example.com/a")
	put("k2", "https://example.com/b")
	put("k3", "https://other.test/c")

	seen := map[string]string{}
	err := c.Walk(ctx, func(key string, meta *cache.EntryMeta) error {
		if _, dup := seen[key]; dup {
			t.Fatalf("duplicate visit for %s", key)
		}
		seen[key] = meta.URL
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(seen), seen)
	}
	if seen["k2"] != "https://example.com/b" {
		t.Fatalf("k2 url: got %q, want https://example.com/b", seen["k2"])
	}
}

func TestCache_Walk_EmptyCache(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)

	called := false
	err := c.Walk(context.Background(), func(key string, meta *cache.EntryMeta) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if called {
		t.Fatal("fn should not be called on empty cache")
	}
}

func TestCache_Walk_PropagatesFnError(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	meta := &cache.EntryMeta{Method: "GET", URL: "https://example.com/x", StatusCode: 200}
	if err := c.Put(ctx, "k1", meta, bytes.NewReader([]byte("b"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Put(ctx, "k2", meta, bytes.NewReader([]byte("b"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	sentinel := errors.New("stop")
	calls := 0
	err := c.Walk(ctx, func(key string, m *cache.EntryMeta) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected walk to stop after 1 call, got %d", calls)
	}
}

func TestCache_Walk_SkipsCorruptMeta(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	// One valid entry.
	meta := &cache.EntryMeta{Method: "GET", URL: "https://example.com/good", StatusCode: 200}
	if err := c.Put(ctx, "good", meta, bytes.NewReader([]byte("b"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// One corrupt meta blob written via raw storage.
	if err := s.Put(ctx, "bad.meta", bytes.NewReader([]byte("not-json"))); err != nil {
		t.Fatalf("inject corrupt meta: %v", err)
	}

	seen := []string{}
	err := c.Walk(ctx, func(key string, m *cache.EntryMeta) error {
		seen = append(seen, key)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 1 || seen[0] != "good" {
		t.Fatalf("expected only 'good', got %v", seen)
	}
}
