package cache_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/index"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

func newTestIndex(t *testing.T) *index.Index {
	t.Helper()
	idx, err := index.Open(filepath.Join(t.TempDir(), "index.db"), index.Options{
		FlushInterval:  100 * time.Millisecond,
		FlushThreshold: 1000,
	})
	if err != nil {
		t.Fatalf("Open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

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

func TestCache_Size_ReturnsBodyByteCount(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	body := []byte("0123456789ab")
	meta := &cache.EntryMeta{Method: "GET", URL: "https://example.com/x", StatusCode: 200}
	if err := c.Put(ctx, "k", meta, bytes.NewReader(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	n, err := c.Size(ctx, "k")
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if n != int64(len(body)) {
		t.Fatalf("got %d, want %d", n, len(body))
	}
}

func TestCache_Size_NotFound(t *testing.T) {
	c := cache.New(storage.NewLocal(t.TempDir()))
	_, err := c.Size(context.Background(), "missing")
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

func TestCache_WithIndex_PutInsertsRow(t *testing.T) {
	idx := newTestIndex(t)
	c := cache.New(storage.NewLocal(t.TempDir())).WithIndex(idx)
	ctx := context.Background()

	meta := &cache.EntryMeta{Method: "GET", URL: "https://example.com/x", StatusCode: 200}
	if err := c.Put(ctx, "k", meta, bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := idx.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Index.Get: %v", err)
	}
	if got.URL != meta.URL || got.Method != "GET" || got.Status != 200 {
		t.Fatalf("wrong row: %+v", got)
	}
	if got.BodySize != 5 {
		t.Fatalf("wrong body_size: got %d, want 5", got.BodySize)
	}
}

func TestCache_WithIndex_GetRecordsHit(t *testing.T) {
	idx := newTestIndex(t)
	c := cache.New(storage.NewLocal(t.TempDir())).WithIndex(idx)
	ctx := context.Background()

	meta := &cache.EntryMeta{Method: "GET", URL: "u", StatusCode: 200}
	c.Put(ctx, "k", meta, bytes.NewReader([]byte("body")))

	_, body, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body.Close()

	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got, _ := idx.Get(ctx, "k")
	if got.HitCount < 1 {
		t.Fatalf("expected hit_count >= 1, got %d", got.HitCount)
	}
}

func TestCache_WithIndex_GetMissDeletesOrphanRow(t *testing.T) {
	idx := newTestIndex(t)
	store := storage.NewLocal(t.TempDir())
	c := cache.New(store).WithIndex(idx)
	ctx := context.Background()

	// Inject an orphan row directly into the index without writing to storage.
	if err := idx.Insert(ctx, index.Entry{
		Key: "ghost", Method: "GET", URL: "u", Status: 200, BodySize: 1,
		CreatedAt: 1, LastAccessedAt: 1,
	}); err != nil {
		t.Fatalf("Insert orphan: %v", err)
	}

	_, _, err := c.Get(ctx, "ghost")
	if err == nil {
		t.Fatal("expected miss")
	}

	_, getErr := idx.Get(ctx, "ghost")
	if !errors.Is(getErr, storage.ErrNotFound) {
		t.Fatalf("orphan row should be deleted; Index.Get err = %v", getErr)
	}
}

func TestEntryMeta_CachedAt_RoundTrip(t *testing.T) {
	want := time.Date(2026, 5, 13, 22, 30, 0, 0, time.UTC)
	meta := &cache.EntryMeta{
		Method:     "GET",
		URL:        "https://example.com/x",
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/plain"}},
		CachedAt:   want,
	}
	data, err := cache.MarshalMeta(meta)
	if err != nil {
		t.Fatalf("MarshalMeta: %v", err)
	}
	got, err := cache.UnmarshalMeta(data)
	if err != nil {
		t.Fatalf("UnmarshalMeta: %v", err)
	}
	if !got.CachedAt.Equal(want) {
		t.Fatalf("CachedAt round-trip: got %v, want %v", got.CachedAt, want)
	}
}

// Legacy entries written before CachedAt existed have no "cached_at" field
// in their JSON. They must unmarshal cleanly (zero value), which the
// revalidate logic interprets as "immediately stale".
func TestEntryMeta_LegacyJSON_NoCachedAt(t *testing.T) {
	legacy := []byte(`{"method":"GET","url":"https://example.com/x","status_code":200,"header":{}}`)
	got, err := cache.UnmarshalMeta(legacy)
	if err != nil {
		t.Fatalf("UnmarshalMeta legacy: %v", err)
	}
	if !got.CachedAt.IsZero() {
		t.Fatalf("legacy CachedAt: got %v, want zero", got.CachedAt)
	}
}

func TestCache_WithIndex_GetReconcilesMissingRow(t *testing.T) {
	idx := newTestIndex(t)
	c := cache.New(storage.NewLocal(t.TempDir())).WithIndex(idx)
	ctx := context.Background()

	meta := &cache.EntryMeta{Method: "GET", URL: "u", StatusCode: 200}
	c.Put(ctx, "k", meta, bytes.NewReader([]byte("body")))

	// Drop the row to simulate post-Put drift (e.g., DB rebuilt from disk).
	if err := idx.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, body, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body.Close()

	// Flush forces RecordHit's buffered upsert to land in SQL.
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got, err := idx.Get(ctx, "k")
	if err != nil {
		t.Fatalf("expected reconciled row, got %v", err)
	}
	if got.URL != meta.URL || got.HitCount < 1 {
		t.Fatalf("reconciled row wrong: %+v", got)
	}
}

func TestCache_WithIndex_DeleteRemovesRow(t *testing.T) {
	idx := newTestIndex(t)
	c := cache.New(storage.NewLocal(t.TempDir())).WithIndex(idx)
	ctx := context.Background()

	meta := &cache.EntryMeta{Method: "GET", URL: "u", StatusCode: 200}
	c.Put(ctx, "k", meta, bytes.NewReader([]byte("body")))

	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := idx.Get(ctx, "k")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("index row should be gone, got %v", err)
	}
}

func TestCache_Reindex_InsertUpdateRemove(t *testing.T) {
	idx := newTestIndex(t)
	store := storage.NewLocal(t.TempDir())
	c := cache.New(store).WithIndex(idx)
	ctx := context.Background()

	// 1) on disk + in index → updated
	meta1 := &cache.EntryMeta{Method: "GET", URL: "u1", StatusCode: 200}
	if err := c.Put(ctx, "indexed", meta1, bytes.NewReader([]byte("body1"))); err != nil {
		t.Fatalf("Put indexed: %v", err)
	}
	// Bump usage stats so we can verify they're preserved.
	idx.Insert(ctx, index.Entry{Key: "indexed", Method: "GET", URL: "u1", Status: 200, BodySize: 5, CreatedAt: 1, LastAccessedAt: 999, HitCount: 42})

	// 2) on disk only (orphan file, no index row) → inserted
	meta2 := &cache.EntryMeta{Method: "POST", URL: "u2", StatusCode: 201}
	metaBytes, _ := cache.MarshalMeta(meta2)
	store.Put(ctx, "ondisk.meta", bytes.NewReader(metaBytes))
	store.Put(ctx, "ondisk.body", bytes.NewReader([]byte("body2")))

	// 3) in index only (no file) → removed
	idx.Insert(ctx, index.Entry{Key: "ghost", Method: "GET", URL: "u3", Status: 200, BodySize: 99, CreatedAt: 1, LastAccessedAt: 1})

	inserted, updated, removed, err := c.Reindex(ctx)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if inserted != 1 || updated != 1 || removed != 1 {
		t.Fatalf("counts: inserted=%d updated=%d removed=%d", inserted, updated, removed)
	}

	// Updated row preserved usage stats.
	got, _ := idx.Get(ctx, "indexed")
	if got.HitCount != 42 || got.LastAccessedAt != 999 || got.CreatedAt != 1 {
		t.Fatalf("stats not preserved: %+v", got)
	}

	// Inserted row exists.
	if _, err := idx.Get(ctx, "ondisk"); err != nil {
		t.Fatalf("expected ondisk reindexed: %v", err)
	}

	// Orphan removed.
	_, err = idx.Get(ctx, "ghost")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ghost removed, got %v", err)
	}
}

func TestCache_Reindex_NoIndexErrors(t *testing.T) {
	c := cache.New(storage.NewLocal(t.TempDir()))
	_, _, _, err := c.Reindex(context.Background())
	if err == nil {
		t.Fatal("expected error when no index attached")
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
