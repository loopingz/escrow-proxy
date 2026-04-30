package index_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/loopingz/escrow-proxy/internal/index"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

func newIndex(t *testing.T) *index.Index {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := index.Open(path, index.Options{
		FlushInterval:  100 * time.Millisecond,
		FlushThreshold: 1000,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func TestOpen_CreatesSchemaAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := index.Open(path, index.Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	idx2, err := index.Open(path, index.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := idx2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestInsertAndGet_RoundTrip(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	want := index.Entry{
		Key:            "abc123",
		Method:         "GET",
		URL:            "https://example.com/x",
		Status:         200,
		BodySize:       1234,
		CreatedAt:      1000,
		LastAccessedAt: 1500,
		HitCount:       3,
	}
	if err := idx.Insert(ctx, want); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := idx.Get(ctx, "abc123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestGet_NotFound(t *testing.T) {
	idx := newIndex(t)
	_, err := idx.Get(context.Background(), "ghost")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestInsert_ReplacesExisting(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	first := index.Entry{Key: "k", Method: "GET", URL: "https://x", Status: 200, BodySize: 100, CreatedAt: 1, LastAccessedAt: 1, HitCount: 0}
	if err := idx.Insert(ctx, first); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	second := index.Entry{Key: "k", Method: "POST", URL: "https://y", Status: 201, BodySize: 200, CreatedAt: 2, LastAccessedAt: 5, HitCount: 7}
	if err := idx.Insert(ctx, second); err != nil {
		t.Fatalf("re-Insert: %v", err)
	}
	got, _ := idx.Get(ctx, "k")
	if got != second {
		t.Fatalf("got %+v, want %+v", got, second)
	}
}

func TestUpsert_PreservesUsageStats(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	original := index.Entry{Key: "k", Method: "GET", URL: "https://x", Status: 200, BodySize: 100, CreatedAt: 1, LastAccessedAt: 50, HitCount: 9}
	if err := idx.Insert(ctx, original); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Upsert refreshes meta/body but should preserve stats.
	upserted := index.Entry{Key: "k", Method: "GET", URL: "https://x", Status: 304, BodySize: 150}
	if err := idx.Upsert(ctx, upserted); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, _ := idx.Get(ctx, "k")
	if got.Status != 304 || got.BodySize != 150 {
		t.Fatalf("expected refreshed status/body_size, got %+v", got)
	}
	if got.CreatedAt != 1 || got.LastAccessedAt != 50 || got.HitCount != 9 {
		t.Fatalf("usage stats not preserved: %+v", got)
	}
}

func TestUpsert_InsertsWhenMissing(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	now := time.Now().Unix()

	e := index.Entry{Key: "k", Method: "GET", URL: "https://x", Status: 200, BodySize: 50}
	if err := idx.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := idx.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CreatedAt < now || got.LastAccessedAt < now {
		t.Fatalf("expected created_at/last_accessed_at >= now, got %+v", got)
	}
	if got.HitCount != 0 {
		t.Fatalf("expected hit_count=0 on new upsert, got %d", got.HitCount)
	}
}

func TestDelete_RemovesRow(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	idx.Insert(ctx, index.Entry{Key: "k", Method: "GET", URL: "u", Status: 200, BodySize: 1, CreatedAt: 1, LastAccessedAt: 1})

	if err := idx.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := idx.Get(ctx, "k")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_MissingKeyIsNoOp(t *testing.T) {
	idx := newIndex(t)
	if err := idx.Delete(context.Background(), "ghost"); err != nil {
		t.Fatalf("Delete on missing key should be nil, got %v", err)
	}
}

func TestList_FilterByExactURL(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	insertSeed(t, idx, []index.Entry{
		{Key: "a", Method: "GET", URL: "https://x/1", Status: 200, BodySize: 10},
		{Key: "b", Method: "POST", URL: "https://x/1", Status: 201, BodySize: 20},
		{Key: "c", Method: "GET", URL: "https://x/2", Status: 200, BodySize: 30},
	})
	got, err := idx.List(ctx, index.ListFilter{URL: "https://x/1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2: %+v", len(got), got)
	}
}

func TestList_FilterByURLPrefix(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	insertSeed(t, idx, []index.Entry{
		{Key: "a", URL: "https://npm.org/x", Method: "GET", Status: 200, BodySize: 10},
		{Key: "b", URL: "https://npm.org/y", Method: "GET", Status: 200, BodySize: 10},
		{Key: "c", URL: "https://pypi.org/z", Method: "GET", Status: 200, BodySize: 10},
	})
	got, err := idx.List(ctx, index.ListFilter{URLPrefix: "https://npm.org/"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestList_FilterByURLAndMethod(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	insertSeed(t, idx, []index.Entry{
		{Key: "g", Method: "GET", URL: "https://x", Status: 200, BodySize: 10},
		{Key: "p", Method: "POST", URL: "https://x", Status: 201, BodySize: 20},
	})
	got, _ := idx.List(ctx, index.ListFilter{URL: "https://x", Method: "GET"})
	if len(got) != 1 || got[0].Key != "g" {
		t.Fatalf("expected only 'g', got %+v", got)
	}
}

func TestList_LimitTruncates(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		idx.Insert(ctx, index.Entry{Key: keyOf(i), URL: "https://x", Method: "GET", Status: 200, BodySize: int64(i)})
	}
	got, _ := idx.List(ctx, index.ListFilter{URL: "https://x", Limit: 3})
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
}

func TestCountAndTotalBodySize(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	insertSeed(t, idx, []index.Entry{
		{Key: "a", URL: "u", Method: "GET", Status: 200, BodySize: 100},
		{Key: "b", URL: "u", Method: "GET", Status: 200, BodySize: 200},
	})
	n, err := idx.Count(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Count: got %d err=%v", n, err)
	}
	total, err := idx.TotalBodySize(ctx)
	if err != nil || total != 300 {
		t.Fatalf("TotalBodySize: got %d err=%v", total, err)
	}
}

func TestLRUEntries_OrderedAscByLastAccessedAt(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	insertSeed(t, idx, []index.Entry{
		{Key: "old", URL: "u1", Method: "GET", Status: 200, BodySize: 1, LastAccessedAt: 100},
		{Key: "mid", URL: "u2", Method: "GET", Status: 200, BodySize: 1, LastAccessedAt: 200},
		{Key: "new", URL: "u3", Method: "GET", Status: 200, BodySize: 1, LastAccessedAt: 300},
	})
	got, err := idx.LRUEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("LRUEntries: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Key != "old" || got[1].Key != "mid" || got[2].Key != "new" {
		t.Fatalf("wrong order: %+v", got)
	}
}

func TestLRUEntries_CutoffFiltersOutFreshEntries(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	insertSeed(t, idx, []index.Entry{
		{Key: "old", URL: "u1", Method: "GET", Status: 200, BodySize: 1, LastAccessedAt: 100},
		{Key: "fresh", URL: "u2", Method: "GET", Status: 200, BodySize: 1, LastAccessedAt: 500},
	})
	// cutoff=200: only entries with last_accessed_at < 200
	got, _ := idx.LRUEntries(ctx, 200, 0)
	if len(got) != 1 || got[0].Key != "old" {
		t.Fatalf("expected only 'old', got %+v", got)
	}
}

func TestLRUEntries_LimitCaps(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		idx.Insert(ctx, index.Entry{Key: keyOf(i), URL: "u", Method: "GET", Status: 200, BodySize: 1, LastAccessedAt: int64(i)})
	}
	got, _ := idx.LRUEntries(ctx, 0, 3)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
}

func TestTouch_FlushUpdatesLastAccessedAndHitCount(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	idx.Insert(ctx, index.Entry{Key: "k", URL: "u", Method: "GET", Status: 200, BodySize: 1, CreatedAt: 1, LastAccessedAt: 1, HitCount: 0})

	idx.Touch("k")
	idx.Touch("k")
	idx.Touch("k")

	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got, _ := idx.Get(ctx, "k")
	if got.HitCount != 3 {
		t.Fatalf("expected hit_count=3, got %d", got.HitCount)
	}
	if got.LastAccessedAt <= 1 {
		t.Fatalf("expected last_accessed_at to advance, got %d", got.LastAccessedAt)
	}
}

func TestTouch_OnMissingKeyIsNoOp(t *testing.T) {
	// Touch on a key the index doesn't know about should not create a row.
	idx := newIndex(t)
	idx.Touch("ghost")
	if err := idx.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	n, _ := idx.Count(context.Background())
	if n != 0 {
		t.Fatalf("expected 0 rows, got %d", n)
	}
}

func TestTouch_ThresholdAutoFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := index.Open(path, index.Options{
		FlushInterval:  1 * time.Hour, // far in the future; not triggered
		FlushThreshold: 5,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer idx.Close()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		idx.Insert(ctx, index.Entry{Key: keyOf(i), URL: "u", Method: "GET", Status: 200, BodySize: 1, CreatedAt: 1, LastAccessedAt: 1})
	}
	for i := 0; i < 5; i++ {
		idx.Touch(keyOf(i))
	}
	// Wait briefly for the flusher goroutine to process the threshold.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := idx.Get(ctx, keyOf(0))
		if got.HitCount == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("threshold flush did not fire within 2s")
}

// ---- helpers ----

func insertSeed(t *testing.T, idx *index.Index, entries []index.Entry) {
	t.Helper()
	ctx := context.Background()
	for _, e := range entries {
		if e.CreatedAt == 0 {
			e.CreatedAt = 1
		}
		if e.LastAccessedAt == 0 {
			e.LastAccessedAt = 1
		}
		if err := idx.Insert(ctx, e); err != nil {
			t.Fatalf("Insert %s: %v", e.Key, err)
		}
	}
}

func keyOf(i int) string {
	return string(rune('a'+i/26)) + string(rune('a'+i%26))
}
