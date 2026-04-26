# Cache Invalidate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `escrow-proxy cache invalidate` CLI subcommand that deletes cache entries by raw key, exact URL, URL prefix, or all-at-once, hitting every configured storage tier. Implements the design at `docs/superpowers/specs/2026-04-24-cache-invalidate-design.md`.

**Architecture:** Two new methods on `*cache.Cache` (`Delete`, `Walk`) plus a new `cmd/escrow-proxy/cache.go` file holding the cobra command tree and a `runInvalidate(ctx, opts)` function that contains all behavior. The CLI uses the existing `loadConfig` / `buildStorage` helpers in `main.go` so it targets the same storage as `serve`. No changes to `internal/storage` — backends already handle idempotent `Delete` and `Tiered` already fans out and dedupes.

**Tech Stack:** Go 1.25, Cobra (CLI), `encoding/json` for `EntryMeta`, existing `internal/storage` and `internal/cache` packages, standard testing with `t.TempDir()` for filesystem isolation.

---

## File Layout

| Path | Action | Responsibility |
|------|--------|----------------|
| `internal/cache/cache.go` | Modify | Add `Delete(ctx, key)` and `Walk(ctx, fn)` methods |
| `internal/cache/cache_test.go` | Modify | Tests for `Delete` and `Walk` |
| `cmd/escrow-proxy/cache.go` | Create | `newCacheCmd`, `newCacheInvalidateCmd`, `runInvalidate(ctx, opts)` |
| `cmd/escrow-proxy/cache_test.go` | Create | End-to-end tests for `runInvalidate` against a temp local-storage tier |
| `cmd/escrow-proxy/main.go` | Modify | One line: `rootCmd.AddCommand(newCacheCmd())` |
| `README.md` | Modify | Add `cache invalidate` to the CLI reference |

---

## Task 1: `Cache.Delete`

**Files:**
- Modify: `internal/cache/cache.go`
- Test: `internal/cache/cache_test.go`

- [ ] **Step 1: Add the failing tests**

Append to `internal/cache/cache_test.go`:

```go
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
```

The new tests need an `errors` import. Check the existing imports at the top of `cache_test.go` — if `errors` is not yet imported, add it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cache -run TestCache_Delete -v`
Expected: FAIL — `c.Delete undefined (type *cache.Cache has no field or method Delete)`.

- [ ] **Step 3: Implement `Delete`**

Add to `internal/cache/cache.go` (below `Exists`):

```go
func (c *Cache) Delete(ctx context.Context, key string) error {
	exists, err := c.storage.Exists(ctx, metaKey(key))
	if err != nil {
		return fmt.Errorf("checking meta: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	if err := c.storage.Delete(ctx, metaKey(key)); err != nil {
		return fmt.Errorf("deleting meta: %w", err)
	}
	if err := c.storage.Delete(ctx, bodyKey(key)); err != nil {
		return fmt.Errorf("deleting body: %w", err)
	}
	return nil
}
```

The package already imports `github.com/loopingz/escrow-proxy/internal/storage`, so `storage.ErrNotFound` resolves without a new import.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache -run TestCache_Delete -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/cache.go internal/cache/cache_test.go
git commit -m "feat(cache): add Delete method"
```

---

## Task 2: `Cache.Walk`

**Files:**
- Modify: `internal/cache/cache.go`
- Test: `internal/cache/cache_test.go`

- [ ] **Step 1: Add the failing tests**

Append to `internal/cache/cache_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cache -run TestCache_Walk -v`
Expected: FAIL — `c.Walk undefined`.

- [ ] **Step 3: Implement `Walk`**

Add to `internal/cache/cache.go` (below `Delete`):

```go
const metaSuffix = ".meta"

func (c *Cache) Walk(ctx context.Context, fn func(key string, meta *EntryMeta) error) error {
	keys, err := c.storage.List(ctx, "")
	if err != nil {
		return fmt.Errorf("listing storage: %w", err)
	}
	for _, k := range keys {
		if !strings.HasSuffix(k, metaSuffix) {
			continue
		}
		cacheKey := strings.TrimSuffix(k, metaSuffix)

		rc, err := c.storage.Get(ctx, k)
		if err != nil {
			continue // skip unreadable entry
		}
		metaBytes, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			continue
		}
		meta, err := UnmarshalMeta(metaBytes)
		if err != nil {
			continue // skip corrupt entry
		}
		if err := fn(cacheKey, meta); err != nil {
			return err
		}
	}
	return nil
}
```

Add `"strings"` to the import block of `cache.go` if not already present (the existing file imports `"bytes"`, `"context"`, `"fmt"`, `"io"`, plus the storage package — `strings` will be new).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cache -run TestCache_Walk -v`
Expected: PASS for all four tests.

Run the full cache package to confirm nothing regressed: `go test ./internal/cache -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/cache.go internal/cache/cache_test.go
git commit -m "feat(cache): add Walk to enumerate entries"
```

---

## Task 3: `runInvalidate` skeleton + filter validation

**Files:**
- Create: `cmd/escrow-proxy/cache.go`
- Create: `cmd/escrow-proxy/cache_test.go`

This task introduces the core `runInvalidate(ctx, opts)` function with no behavior except flag validation, plus a test scaffold. Cobra wiring comes later (Task 9).

- [ ] **Step 1: Create `cmd/escrow-proxy/cache.go` with the options struct and validation-only body**

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/loopingz/escrow-proxy/internal/cache"
)

// invalidateOptions carries everything runInvalidate needs.
// The CLI layer constructs this; tests construct it directly.
type invalidateOptions struct {
	Cache *cache.Cache

	Key       string
	URL       string
	URLPrefix string
	All       bool
	Method    string
	DryRun    bool

	Stdout io.Writer
	Stderr io.Writer
}

// runInvalidate executes the cache invalidate command.
// Returns nil on success, an error otherwise. Per-entry failures during
// bulk operations are logged to Stderr and counted; the function returns
// an error if any entry failed to delete.
func runInvalidate(ctx context.Context, opts invalidateOptions) error {
	if err := validateInvalidateFilters(opts); err != nil {
		return err
	}
	return errors.New("not implemented")
}

func validateInvalidateFilters(opts invalidateOptions) error {
	count := 0
	if opts.Key != "" {
		count++
	}
	if opts.URL != "" {
		count++
	}
	if opts.URLPrefix != "" {
		count++
	}
	if opts.All {
		count++
	}
	if count == 0 {
		return fmt.Errorf("exactly one of --key, --url, --url-prefix, --all must be specified")
	}
	if count > 1 {
		return fmt.Errorf("flags --key, --url, --url-prefix, --all are mutually exclusive")
	}
	if opts.All && opts.Method != "" {
		return fmt.Errorf("--method cannot be combined with --all")
	}
	return nil
}
```

- [ ] **Step 2: Create `cmd/escrow-proxy/cache_test.go` with the validation tests and a helper**

```go
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
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate -v`
Expected: PASS for all three validation tests.

- [ ] **Step 4: Commit**

```bash
git add cmd/escrow-proxy/cache.go cmd/escrow-proxy/cache_test.go
git commit -m "feat(cli): scaffold cache invalidate with filter validation"
```

---

## Task 4: `--key` filter

**Files:**
- Modify: `cmd/escrow-proxy/cache.go`
- Modify: `cmd/escrow-proxy/cache_test.go`

- [ ] **Step 1: Add the failing tests**

Append to `cmd/escrow-proxy/cache_test.go`:

```go
func TestInvalidate_Key_DeletesSingleEntry(t *testing.T) {
	c, s, put := newTestCache(t)
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
	_ = s
}

func TestInvalidate_Key_MissingReturnsError(t *testing.T) {
	c, _, _ := newTestCache(t)
	_, _, err := runOpts(c, func(o *invalidateOptions) { o.Key = "ghost" })
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound wrap, got %v", err)
	}
}
```

The test file's import block needs `errors` — add it. The existing `_ = io.Discard` line can be removed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate_Key -v`
Expected: FAIL — `runInvalidate` returns `not implemented`.

- [ ] **Step 3: Implement the `--key` path using the final code shape**

Replace the entire body of `runInvalidate` in `cmd/escrow-proxy/cache.go` with the dispatch shape below, and add `invalidateTarget`, `collectTargets`, and `executeDeletes` helpers. (Task 5 adds `scanTargets` and the URL case; later tasks add more cases. The shape stays the same.)

```go
func runInvalidate(ctx context.Context, opts invalidateOptions) error {
	if err := validateInvalidateFilters(opts); err != nil {
		return err
	}

	targets, err := collectTargets(ctx, opts)
	if err != nil {
		return err
	}
	return executeDeletes(ctx, opts, targets)
}

type invalidateTarget struct {
	Key    string
	Method string
	URL    string
}

func collectTargets(ctx context.Context, opts invalidateOptions) ([]invalidateTarget, error) {
	switch {
	case opts.Key != "":
		meta, body, err := opts.Cache.Get(ctx, opts.Key)
		if err != nil {
			return nil, fmt.Errorf("locating entry: %w", err)
		}
		body.Close()
		return []invalidateTarget{{Key: opts.Key, Method: meta.Method, URL: meta.URL}}, nil
	}
	return nil, errors.New("not implemented") // filled in by later tasks
}

func executeDeletes(ctx context.Context, opts invalidateOptions, targets []invalidateTarget) error {
	verb := "deleted"
	if opts.DryRun {
		verb = "would delete"
	}

	failed := 0
	for _, tg := range targets {
		if !opts.DryRun {
			if err := opts.Cache.Delete(ctx, tg.Key); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					continue // race with concurrent delete; not a failure
				}
				fmt.Fprintf(opts.Stderr, "failed to delete %s: %v\n", tg.Key, err)
				failed++
				continue
			}
		}
		fmt.Fprintf(opts.Stdout, "%s %s %s\n", tg.Key, tg.Method, tg.URL)
	}

	fmt.Fprintf(opts.Stderr, "%s %d entries\n", verb, len(targets)-failed)
	if failed > 0 {
		return fmt.Errorf("%d entries failed to delete", failed)
	}
	return nil
}
```

Add `"github.com/loopingz/escrow-proxy/internal/storage"` to the import block. The `cache` package import is already present from Task 3.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate -v`
Expected: PASS for `TestInvalidate_ZeroFilters`, `TestInvalidate_MultipleFilters`, `TestInvalidate_AllWithMethodRejected`, `TestInvalidate_Key_DeletesSingleEntry`, `TestInvalidate_Key_MissingReturnsError`.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-proxy/cache.go cmd/escrow-proxy/cache_test.go
git commit -m "feat(cli): implement cache invalidate --key"
```

---

## Task 5: `--url` filter (with optional `--method`)

**Files:**
- Modify: `cmd/escrow-proxy/cache.go`
- Modify: `cmd/escrow-proxy/cache_test.go`

- [ ] **Step 1: Add the failing tests**

Append to `cmd/escrow-proxy/cache_test.go`:

```go
func TestInvalidate_URL_DeletesAllVariations(t *testing.T) {
	c, _, put := newTestCache(t)
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
	c, _, put := newTestCache(t)
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
	c, _, put := newTestCache(t)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate_URL -v`
Expected: FAIL — `--url` path returns `not implemented`.

- [ ] **Step 3: Add `scanTargets` and the `--url` case**

In `cmd/escrow-proxy/cache.go`, add `scanTargets` between `collectTargets` and `executeDeletes`:

```go
func scanTargets(ctx context.Context, c *cache.Cache, match func(*cache.EntryMeta) bool) ([]invalidateTarget, error) {
	var out []invalidateTarget
	err := c.Walk(ctx, func(key string, meta *cache.EntryMeta) error {
		if match(meta) {
			out = append(out, invalidateTarget{Key: key, Method: meta.Method, URL: meta.URL})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning cache: %w", err)
	}
	return out, nil
}
```

Then add the `--url` case to `collectTargets`. After Task 5 the function reads:

```go
func collectTargets(ctx context.Context, opts invalidateOptions) ([]invalidateTarget, error) {
	switch {
	case opts.Key != "":
		meta, body, err := opts.Cache.Get(ctx, opts.Key)
		if err != nil {
			return nil, fmt.Errorf("locating entry: %w", err)
		}
		body.Close()
		return []invalidateTarget{{Key: opts.Key, Method: meta.Method, URL: meta.URL}}, nil
	case opts.URL != "":
		return scanTargets(ctx, opts.Cache, func(meta *cache.EntryMeta) bool {
			if opts.Method != "" && !strings.EqualFold(meta.Method, opts.Method) {
				return false
			}
			return meta.URL == opts.URL
		})
	}
	return nil, errors.New("not implemented") // filled in by later tasks
}
```

Add `"strings"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate -v`
Expected: PASS for all existing tests plus the three new `--url` tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-proxy/cache.go cmd/escrow-proxy/cache_test.go
git commit -m "feat(cli): implement cache invalidate --url and --method"
```

---

## Task 6: `--url-prefix` filter

**Files:**
- Modify: `cmd/escrow-proxy/cache.go`
- Modify: `cmd/escrow-proxy/cache_test.go`

- [ ] **Step 1: Add the failing tests**

Append to `cmd/escrow-proxy/cache_test.go`:

```go
func TestInvalidate_URLPrefix_DeletesMatching(t *testing.T) {
	c, _, put := newTestCache(t)
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
	c, _, put := newTestCache(t)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate_URLPrefix -v`
Expected: FAIL — `--url-prefix` path returns `not implemented`.

- [ ] **Step 3: Implement the `--url-prefix` case**

In `collectTargets` in `cmd/escrow-proxy/cache.go`, add a case after `case opts.URL != "":` and before the trailing `return nil, errors.New("not implemented")`:

```go
case opts.URLPrefix != "":
    return scanTargets(ctx, opts.Cache, func(meta *cache.EntryMeta) bool {
        if opts.Method != "" && !strings.EqualFold(meta.Method, opts.Method) {
            return false
        }
        return strings.HasPrefix(meta.URL, opts.URLPrefix)
    })
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate -v`
Expected: PASS for all tests so far, including the two new prefix tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-proxy/cache.go cmd/escrow-proxy/cache_test.go
git commit -m "feat(cli): implement cache invalidate --url-prefix"
```

---

## Task 7: `--all` filter

**Files:**
- Modify: `cmd/escrow-proxy/cache.go`
- Modify: `cmd/escrow-proxy/cache_test.go`

- [ ] **Step 1: Add the failing test**

Append to `cmd/escrow-proxy/cache_test.go`:

```go
func TestInvalidate_All_DeletesEverything(t *testing.T) {
	c, _, put := newTestCache(t)
	put("a", "GET", "https://example.com/a")
	put("b", "POST", "https://example.com/b")
	put("c", "GET", "https://other.test/c")

	_, stderr, err := runOpts(c, func(o *invalidateOptions) { o.All = true })
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}

	for _, k := range []string{"a", "b", "c"} {
		exists, _ := c.Exists(context.Background(), k)
		if exists {
			t.Fatalf("%s should be gone", k)
		}
	}
	if !strings.Contains(stderr, "deleted 3 entries") {
		t.Fatalf("expected 'deleted 3 entries', got %q", stderr)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate_All -v`
Expected: FAIL — `--all` returns `not implemented`.

- [ ] **Step 3: Implement the `--all` case**

In `collectTargets`, add the final case before the trailing return. After this task the function reads:

```go
func collectTargets(ctx context.Context, opts invalidateOptions) ([]invalidateTarget, error) {
	switch {
	case opts.Key != "":
		meta, body, err := opts.Cache.Get(ctx, opts.Key)
		if err != nil {
			return nil, fmt.Errorf("locating entry: %w", err)
		}
		body.Close()
		return []invalidateTarget{{Key: opts.Key, Method: meta.Method, URL: meta.URL}}, nil
	case opts.URL != "":
		return scanTargets(ctx, opts.Cache, func(meta *cache.EntryMeta) bool {
			if opts.Method != "" && !strings.EqualFold(meta.Method, opts.Method) {
				return false
			}
			return meta.URL == opts.URL
		})
	case opts.URLPrefix != "":
		return scanTargets(ctx, opts.Cache, func(meta *cache.EntryMeta) bool {
			if opts.Method != "" && !strings.EqualFold(meta.Method, opts.Method) {
				return false
			}
			return strings.HasPrefix(meta.URL, opts.URLPrefix)
		})
	case opts.All:
		return scanTargets(ctx, opts.Cache, func(*cache.EntryMeta) bool { return true })
	}
	// unreachable: validateInvalidateFilters guarantees exactly one filter is set.
	return nil, errors.New("no filter matched")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate -v`
Expected: PASS for every existing test plus the new `--all` test.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-proxy/cache.go cmd/escrow-proxy/cache_test.go
git commit -m "feat(cli): implement cache invalidate --all"
```

---

## Task 8: `--dry-run` mode

**Files:**
- Modify: `cmd/escrow-proxy/cache_test.go`

The `executeDeletes` function written in Task 5 already honors `opts.DryRun` (skips the actual delete call and uses the verb "would delete"). This task adds explicit test coverage and verifies storage stays untouched.

- [ ] **Step 1: Add the failing tests**

Append to `cmd/escrow-proxy/cache_test.go`:

```go
func TestInvalidate_DryRun_All_PrintsButLeavesStorage(t *testing.T) {
	c, _, put := newTestCache(t)
	put("a", "GET", "https://example.com/a")
	put("b", "GET", "https://example.com/b")

	stdout, stderr, err := runOpts(c, func(o *invalidateOptions) {
		o.All = true
		o.DryRun = true
	})
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}

	for _, k := range []string{"a", "b"} {
		exists, _ := c.Exists(context.Background(), k)
		if !exists {
			t.Fatalf("%s should still exist after --dry-run", k)
		}
	}

	lines := strings.Count(strings.TrimSpace(stdout), "\n") + 1
	if lines != 2 {
		t.Fatalf("expected 2 stdout lines, got %d: %q", lines, stdout)
	}
	if !strings.Contains(stderr, "would delete 2 entries") {
		t.Fatalf("expected 'would delete 2 entries', got %q", stderr)
	}
}

func TestInvalidate_DryRun_Key_PrintsExactlyOne(t *testing.T) {
	c, _, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://example.com/b")

	stdout, stderr, err := runOpts(c, func(o *invalidateOptions) {
		o.Key = "k1"
		o.DryRun = true
	})
	if err != nil {
		t.Fatalf("runInvalidate: %v", err)
	}
	exists, _ := c.Exists(context.Background(), "k1")
	if !exists {
		t.Fatal("k1 should still exist after --dry-run")
	}
	if !strings.Contains(stdout, "k1") || !strings.Contains(stdout, "https://example.com/a") {
		t.Fatalf("stdout missing entry details: %q", stdout)
	}
	if !strings.Contains(stderr, "would delete 1 entries") {
		t.Fatalf("expected 'would delete 1 entries', got %q", stderr)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./cmd/escrow-proxy -run TestInvalidate_DryRun -v`
Expected: PASS for both new tests (the production code already handles `DryRun`).

- [ ] **Step 3: Commit**

```bash
git add cmd/escrow-proxy/cache_test.go
git commit -m "test(cli): cover cache invalidate --dry-run"
```

---

## Task 9: Cobra wiring + register in main

**Files:**
- Modify: `cmd/escrow-proxy/cache.go`
- Modify: `cmd/escrow-proxy/main.go`

- [ ] **Step 1: Add `"github.com/spf13/cobra"` to the import block of `cmd/escrow-proxy/cache.go`**

After this task the file's import block is:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/storage"
	"github.com/spf13/cobra"
)
```

- [ ] **Step 2: Append cobra constructors to `cmd/escrow-proxy/cache.go`**

```go
func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Cache management commands",
	}
	cmd.AddCommand(newCacheInvalidateCmd())
	return cmd
}

func newCacheInvalidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invalidate",
		Short: "Delete cache entries by key, URL, URL prefix, or all",
		Long: `Delete cache entries from every configured storage tier.

Exactly one of --key, --url, --url-prefix, --all must be supplied.
--method narrows --url and --url-prefix to a specific HTTP method.
--dry-run reports what would be deleted without touching storage.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}
			c := cache.New(store)

			key, _ := cmd.Flags().GetString("key")
			url, _ := cmd.Flags().GetString("url")
			urlPrefix, _ := cmd.Flags().GetString("url-prefix")
			all, _ := cmd.Flags().GetBool("all")
			method, _ := cmd.Flags().GetString("method")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			return runInvalidate(cmd.Context(), invalidateOptions{
				Cache:     c,
				Key:       key,
				URL:       url,
				URLPrefix: urlPrefix,
				All:       all,
				Method:    method,
				DryRun:    dryRun,
				Stdout:    cmd.OutOrStdout(),
				Stderr:    cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().String("key", "", "exact cache key (hex SHA256)")
	cmd.Flags().String("url", "", "exact request URL")
	cmd.Flags().String("url-prefix", "", "request URL prefix")
	cmd.Flags().Bool("all", false, "delete every entry")
	cmd.Flags().String("method", "", "narrow --url/--url-prefix to a specific HTTP method")
	cmd.Flags().Bool("dry-run", false, "print what would be deleted without deleting")
	return cmd
}
```

- [ ] **Step 3: Register in `cmd/escrow-proxy/main.go`**

In `main()` of `cmd/escrow-proxy/main.go`, after the existing `rootCmd.AddCommand(newCACmd())` line, add:

```go
rootCmd.AddCommand(newCacheCmd())
```

- [ ] **Step 4: Add a smoke test that the command parses and routes correctly**

Append to `cmd/escrow-proxy/cache_test.go`:

```go
func TestCacheInvalidate_HelpExits0(t *testing.T) {
	cmd := newCacheCmd()
	cmd.SetArgs([]string{"invalidate", "--help"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help should exit 0, got %v", err)
	}
}
```

- [ ] **Step 5: Build and run all tests**

Run: `go build ./...`
Expected: builds clean.

Run: `go test ./...`
Expected: PASS across the project.

Run a manual smoke check:

```bash
go run ./cmd/escrow-proxy cache invalidate --help
```

Expected: usage text printed; exit 0.

- [ ] **Step 6: Commit**

```bash
git add cmd/escrow-proxy/cache.go cmd/escrow-proxy/cache_test.go cmd/escrow-proxy/main.go
git commit -m "feat(cli): wire cache invalidate into root command"
```

---

## Task 10: README documentation

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a `Cache Management` subsection**

Insert after the existing "Offline" subsection in the "Modes" section of `README.md` (between the existing "Offline" block and "Request Flow"). The exact text to add to `README.md` is below (rendered between the two `~~~markdown` fences so the plan's own markdown parser does not eat the embedded triple-backticks):

~~~markdown
### Cache management

Delete cache entries without restarting the proxy. The CLI hits every configured storage tier (matching the write path), so an entry deleted here will not backfill from L2.

```bash
# Delete one entry by raw key (from logs: "cache hit ... key=<hex>")
escrow-proxy cache invalidate --key 8d3f7a...

# Delete every cached variation of a URL (across methods/headers)
escrow-proxy cache invalidate --url https://registry.npmjs.org/lodash

# Narrow to a single HTTP method
escrow-proxy cache invalidate --url https://registry.npmjs.org/lodash --method GET

# Bulk: every entry under a URL prefix
escrow-proxy cache invalidate --url-prefix https://registry.npmjs.org/

# Nuke the entire cache
escrow-proxy cache invalidate --all

# Preview without deleting
escrow-proxy cache invalidate --url-prefix https://registry.npmjs.org/ --dry-run
```

Exactly one of `--key`, `--url`, `--url-prefix`, `--all` is required. The command uses the same storage flags (`--storage`, `--local-dir`, `--gcs-bucket`, etc.) as `serve`.
~~~

- [ ] **Step 2: Verify rendering**

Run: `git diff README.md`
Confirm the new subsection sits in the right place and the fenced code block is balanced.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document cache invalidate subcommand"
```

---

## Done criteria

- `go build ./...` clean.
- `go test ./...` passes.
- `escrow-proxy cache invalidate --help` prints usage.
- All four filter modes (`--key`, `--url`, `--url-prefix`, `--all`) behave per spec.
- `--method` narrows `--url` and `--url-prefix`; rejected with `--all`.
- `--dry-run` lists matches without touching storage.
- Tiered storage delete matches write path (verified by existing `Tiered` tests; no behavior change required).
- README documents the command.
