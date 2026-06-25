package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/index"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// newTestCacheWithIndex returns a Cache backed by a fresh tempdir local
// store, attached to a fresh SQLite index, plus a put helper.
func newTestCacheWithIndex(t *testing.T) (*cache.Cache, func(key, method, url string)) {
	t.Helper()
	store := storage.NewLocal(t.TempDir())
	idx, err := index.Open(filepath.Join(t.TempDir(), "index.db"), index.Options{
		FlushInterval:  100 * time.Millisecond,
		FlushThreshold: 1000,
	})
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	c := cache.New(store).WithIndex(idx)
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

func TestInvalidate_All_DeletesEverything(t *testing.T) {
	c, put := newTestCache(t)
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

func TestInvalidate_DryRun_All_PrintsButLeavesStorage(t *testing.T) {
	c, put := newTestCache(t)
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
	c, put := newTestCache(t)
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

func TestCacheInvalidate_HelpExits0(t *testing.T) {
	cmd := newCacheCmd()
	cmd.SetArgs([]string{"invalidate", "--help"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help should exit 0, got %v", err)
	}
}

// ---- list ----

func runListOpts(c *cache.Cache, mutate func(*listOptions)) (string, string, error) {
	var stdout, stderr bytes.Buffer
	opts := listOptions{
		Cache:  c,
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if mutate != nil {
		mutate(&opts)
	}
	err := runList(context.Background(), opts)
	return stdout.String(), stderr.String(), err
}

func TestList_NoFilter_ListsAllUnderImplicitCap(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "POST", "https://example.com/b")
	put("k3", "GET", "https://other.test/c")

	stdout, stderr, err := runListOpts(c, nil)
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), stdout)
	}
	for _, want := range []string{"k1 GET 200", "k2 POST 200", "k3 GET 200"} {
		if !linesContain(lines, want) {
			t.Fatalf("expected a line containing %q, got %q", want, stdout)
		}
	}
	if strings.Contains(stderr, "truncated") {
		t.Fatalf("did not expect truncation note: %q", stderr)
	}
}

func TestList_NoFilter_ImplicitCap1000(t *testing.T) {
	c, put := newTestCache(t)
	for i := 0; i < 1001; i++ {
		put(fmt.Sprintf("k%04d", i), "GET", fmt.Sprintf("https://example.com/%d", i))
	}
	stdout, stderr, err := runListOpts(c, nil)
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 1000 {
		t.Fatalf("expected 1000 lines, got %d", len(lines))
	}
	if !strings.Contains(stderr, "truncated at 1000 entries") {
		t.Fatalf("expected truncation note, got %q", stderr)
	}
}

func TestList_NoFilter_ExplicitLimitZeroDisablesCap(t *testing.T) {
	c, put := newTestCache(t)
	for i := 0; i < 1001; i++ {
		put(fmt.Sprintf("k%04d", i), "GET", fmt.Sprintf("https://example.com/%d", i))
	}
	stdout, stderr, err := runListOpts(c, func(o *listOptions) {
		o.Limit = 0
		o.LimitSet = true
	})
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 1001 {
		t.Fatalf("expected 1001 lines, got %d", len(lines))
	}
	if strings.Contains(stderr, "truncated") {
		t.Fatalf("did not expect truncation note: %q", stderr)
	}
}

func TestList_ExplicitLimitTruncates(t *testing.T) {
	c, put := newTestCache(t)
	for i := 0; i < 5; i++ {
		put(fmt.Sprintf("k%d", i), "GET", fmt.Sprintf("https://example.com/%d", i))
	}
	stdout, stderr, err := runListOpts(c, func(o *listOptions) {
		o.Limit = 2
		o.LimitSet = true
	})
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if !strings.Contains(stderr, "truncated at 2 entries") {
		t.Fatalf("expected truncation note, got %q", stderr)
	}
}

func TestList_URL_ExactMatch(t *testing.T) {
	c, put := newTestCache(t)
	put("get", "GET", "https://example.com/x")
	put("post", "POST", "https://example.com/x")
	put("other", "GET", "https://example.com/y")

	stdout, _, err := runListOpts(c, func(o *listOptions) {
		o.URL = "https://example.com/x"
	})
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), stdout)
	}
	for _, k := range []string{"get", "post"} {
		if !linesContain(lines, k+" ") {
			t.Fatalf("expected %s in output, got %q", k, stdout)
		}
	}
}

func TestList_URLPrefix_Match(t *testing.T) {
	c, put := newTestCache(t)
	put("a", "GET", "https://npmjs.org/pkg/foo")
	put("b", "GET", "https://npmjs.org/pkg/bar")
	put("c", "GET", "https://pypi.org/simple/baz")

	stdout, _, err := runListOpts(c, func(o *listOptions) {
		o.URLPrefix = "https://npmjs.org/"
	})
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
}

func TestList_URLPrefix_WithMethod(t *testing.T) {
	c, put := newTestCache(t)
	put("get1", "GET", "https://npmjs.org/pkg/foo")
	put("post1", "POST", "https://npmjs.org/pkg/foo")
	put("get2", "GET", "https://npmjs.org/pkg/bar")

	stdout, _, err := runListOpts(c, func(o *listOptions) {
		o.URLPrefix = "https://npmjs.org/"
		o.Method = "GET"
	})
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), stdout)
	}
	if linesContain(lines, "post1 ") {
		t.Fatal("post1 should not appear")
	}
}

func TestList_Key_SingleEntry(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://example.com/b")

	stdout, _, err := runListOpts(c, func(o *listOptions) { o.Key = "k1" })
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), stdout)
	}
	if !strings.Contains(stdout, "k1") || !strings.Contains(stdout, "https://example.com/a") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
}

func TestList_Key_NotFound(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runListOpts(c, func(o *listOptions) { o.Key = "ghost" })
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestList_BodySizeColumnIsByteCount(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a") // body = "body-k1" → 7 bytes

	stdout, _, err := runListOpts(c, nil)
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	// columns: <key> <method> <status> <body-bytes> <url>
	fields := strings.Fields(strings.TrimSpace(stdout))
	if len(fields) < 5 {
		t.Fatalf("expected >=5 fields, got %v", fields)
	}
	if fields[3] != "7" {
		t.Fatalf("expected body-size column = 7, got %q (full: %q)", fields[3], stdout)
	}
}

func TestList_JSON_Output(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	stdout, _, err := runListOpts(c, func(o *listOptions) { o.JSON = true })
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 1 {
		t.Fatalf("expected 1 NDJSON line, got %d", len(lines))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("invalid JSON: %v (line: %q)", err, lines[0])
	}
	for _, k := range []string{"key", "method", "url", "status", "body_size"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing field %q in JSON: %v", k, got)
		}
	}
	if got["key"] != "k1" || got["method"] != "GET" || got["url"] != "https://example.com/a" {
		t.Fatalf("wrong values: %v", got)
	}
}

func TestList_MutualExclusion(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runListOpts(c, func(o *listOptions) {
		o.URL = "x"
		o.Key = "y"
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestList_MethodWithoutURL(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runListOpts(c, func(o *listOptions) { o.Method = "GET" })
	if err == nil || !strings.Contains(err.Error(), "--method") {
		t.Fatalf("expected --method error, got %v", err)
	}
}

func TestList_WithIndex_URLPrefixMatch(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("a", "GET", "https://npm.org/x")
	put("b", "GET", "https://npm.org/y")
	put("c", "GET", "https://pypi.org/z")

	stdout, _, err := runListOpts(c, func(o *listOptions) { o.URLPrefix = "https://npm.org/" })
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("expected 2, got %d: %q", len(lines), stdout)
	}
}

func TestList_WithIndex_BodySizeFromIndex(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("k1", "GET", "https://example.com/a") // body = "body-k1" → 7 bytes

	stdout, _, err := runListOpts(c, func(o *listOptions) { o.URL = "https://example.com/a" })
	if err != nil {
		t.Fatalf("runList: %v", err)
	}
	fields := strings.Fields(strings.TrimSpace(stdout))
	if len(fields) < 5 || fields[3] != "7" {
		t.Fatalf("expected body-size 7, got %q", stdout)
	}
}

func TestShow_WithIndex_URLAmbiguityListsCandidates(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("get", "GET", "https://example.com/x")
	put("post", "POST", "https://example.com/x")

	_, stderr, err := runShowOpts(c, func(o *showOptions) { o.URL = "https://example.com/x" })
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	for _, k := range []string{"get", "post"} {
		if !strings.Contains(stderr, k) {
			t.Fatalf("expected stderr to list %s, got %q", k, stderr)
		}
	}
}

func TestList_NegativeLimitRejected(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runListOpts(c, func(o *listOptions) {
		o.Limit = -1
		o.LimitSet = true
	})
	if err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Fatalf("expected limit validation error, got %v", err)
	}
}

// ---- show ----

func runShowOpts(c *cache.Cache, mutate func(*showOptions)) (string, string, error) {
	var stdout, stderr bytes.Buffer
	opts := showOptions{
		Cache:  c,
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if mutate != nil {
		mutate(&opts)
	}
	err := runShow(context.Background(), opts)
	return stdout.String(), stderr.String(), err
}

func TestShow_Key_PrintsMetadataBlock(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	stdout, _, err := runShowOpts(c, func(o *showOptions) { o.Key = "k1" })
	if err != nil {
		t.Fatalf("runShow: %v", err)
	}
	for _, want := range []string{"key:", "k1", "method:", "GET", "url:", "https://example.com/a", "status:", "200", "body-size:", "7", "headers:", "Content-Type"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in stdout: %q", want, stdout)
		}
	}
}

func TestShow_Key_NotFound(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runShowOpts(c, func(o *showOptions) { o.Key = "ghost" })
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestShow_URL_SingleMatch(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://example.com/b")

	stdout, _, err := runShowOpts(c, func(o *showOptions) {
		o.URL = "https://example.com/a"
	})
	if err != nil {
		t.Fatalf("runShow: %v", err)
	}
	if !strings.Contains(stdout, "k1") {
		t.Fatalf("expected k1 in stdout: %q", stdout)
	}
}

func TestShow_URL_MultipleMatches_AmbiguityError(t *testing.T) {
	c, put := newTestCache(t)
	put("get", "GET", "https://example.com/x")
	put("post", "POST", "https://example.com/x")

	_, stderr, err := runShowOpts(c, func(o *showOptions) {
		o.URL = "https://example.com/x"
	})
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	for _, k := range []string{"get", "post"} {
		if !strings.Contains(stderr, k) {
			t.Fatalf("expected stderr to list %s, got %q", k, stderr)
		}
	}
}

func TestShow_URL_NoMatch(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	_, _, err := runShowOpts(c, func(o *showOptions) {
		o.URL = "https://nowhere.test/"
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestShow_JSON_Output(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	stdout, _, err := runShowOpts(c, func(o *showOptions) {
		o.Key = "k1"
		o.JSON = true
	})
	if err != nil {
		t.Fatalf("runShow: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("invalid JSON: %v (stdout: %q)", err, stdout)
	}
	for _, k := range []string{"key", "method", "url", "status", "body_size", "headers"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing field %q in JSON: %v", k, got)
		}
	}
	if got["key"] != "k1" {
		t.Fatalf("wrong key: %v", got)
	}
}

func TestShow_BodyToOutputFile(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	dst := filepath.Join(t.TempDir(), "out.bin")
	_, stderr, err := runShowOpts(c, func(o *showOptions) {
		o.Key = "k1"
		o.Output = dst
	})
	if err != nil {
		t.Fatalf("runShow: %v", err)
	}

	got, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatalf("read output: %v", readErr)
	}
	if string(got) != "body-k1" {
		t.Fatalf("got %q, want %q", got, "body-k1")
	}
	if !strings.Contains(stderr, "wrote 7 bytes") {
		t.Fatalf("expected stderr confirmation, got %q", stderr)
	}
}

func TestShow_BodyToTTY_Refused(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	_, _, err := runShowOpts(c, func(o *showOptions) {
		o.Key = "k1"
		o.Body = true
		o.StdoutIsTTY = true
	})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected TTY refusal, got %v", err)
	}
}

func TestShow_BodyToNonTTY_AppendsToStdout(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	stdout, _, err := runShowOpts(c, func(o *showOptions) {
		o.Key = "k1"
		o.Body = true
		o.StdoutIsTTY = false
	})
	if err != nil {
		t.Fatalf("runShow: %v", err)
	}
	if !strings.Contains(stdout, "body-k1") {
		t.Fatalf("expected body in stdout: %q", stdout)
	}
}

func TestShow_OutputImpliesBody(t *testing.T) {
	// --output without --body should still write file; --body left unset.
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")

	dst := filepath.Join(t.TempDir(), "out.bin")
	_, _, err := runShowOpts(c, func(o *showOptions) {
		o.Key = "k1"
		o.Output = dst
		o.Body = false
	})
	if err != nil {
		t.Fatalf("runShow: %v", err)
	}
	got, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatalf("read output: %v", readErr)
	}
	if string(got) != "body-k1" {
		t.Fatalf("got %q, want body-k1", got)
	}
}

func TestShow_KeyAndURLMutuallyExclusive(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runShowOpts(c, func(o *showOptions) {
		o.Key = "k"
		o.URL = "x"
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestShow_MissingKeyAndURL(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runShowOpts(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--key or --url") {
		t.Fatalf("expected required-flag error, got %v", err)
	}
}

// ---- parsers ----

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"1K", 1024},
		{"1k", 1024},
		{"1M", 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"1T", 1024 * 1024 * 1024 * 1024},
		{"1.5G", int64(1.5 * 1024 * 1024 * 1024)},
		{"100M", 100 * 1024 * 1024},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if err != nil {
			t.Errorf("parseSize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q): got %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSize_Invalid(t *testing.T) {
	for _, in := range []string{"", "abc", "1.5.6G", "G"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q): expected error", in)
		}
	}
}

func TestParseAge(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"1.5d", time.Duration(1.5 * 24 * float64(time.Hour))},
	}
	for _, c := range cases {
		got, err := parseAge(c.in)
		if err != nil {
			t.Errorf("parseAge(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseAge(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}

// ---- evict ----

func runEvictOpts(c *cache.Cache, mutate func(*evictOptions)) (string, string, error) {
	var stdout, stderr bytes.Buffer
	opts := evictOptions{Cache: c, Stdout: &stdout, Stderr: &stderr}
	if mutate != nil {
		mutate(&opts)
	}
	err := runEvict(context.Background(), opts)
	return stdout.String(), stderr.String(), err
}

func TestEvict_RequiresTargetSize(t *testing.T) {
	c, _ := newTestCacheWithIndex(t)
	_, _, err := runEvictOpts(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--target-size") {
		t.Fatalf("expected target-size error, got %v", err)
	}
}

func TestEvict_NoIndexErrors(t *testing.T) {
	c, _ := newTestCache(t) // no index
	_, _, err := runEvictOpts(c, func(o *evictOptions) { o.TargetSizeSet = true; o.TargetSize = 100 })
	if err == nil || !strings.Contains(err.Error(), "index") {
		t.Fatalf("expected index error, got %v", err)
	}
}

func TestEvict_NothingToDoWhenUnderTarget(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("k1", "GET", "https://x")
	_, stderr, err := runEvictOpts(c, func(o *evictOptions) {
		o.TargetSizeSet = true
		o.TargetSize = 1024 * 1024
	})
	if err != nil {
		t.Fatalf("runEvict: %v", err)
	}
	if !strings.Contains(stderr, "nothing to evict") {
		t.Fatalf("expected 'nothing to evict' in stderr, got %q", stderr)
	}
}

func TestEvict_LRUOrderUntilTargetReached(t *testing.T) {
	c, _ := newTestCacheWithIndex(t)
	idx := c.Index()
	ctx := context.Background()

	// Seed three entries with explicit last_accessed_at so order is deterministic.
	for i, k := range []string{"old", "mid", "new"} {
		meta := &cache.EntryMeta{Method: "GET", URL: "u" + k, StatusCode: 200}
		if err := c.Put(ctx, k, meta, bytes.NewReader([]byte("body-"+k))); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
		// Override the last_accessed_at the Put set with a deterministic one.
		if err := idx.Insert(ctx, index.Entry{
			Key: k, Method: "GET", URL: "u" + k, Status: 200,
			BodySize: int64(len("body-" + k)),
			CreatedAt: 1, LastAccessedAt: int64(100 + i*100),
		}); err != nil {
			t.Fatalf("override insert: %v", err)
		}
	}

	// Total = 3*7 = 21 bytes. Target=10 → need to free 11 bytes. Each entry is 7,
	// so we should evict 'old' and 'mid' (= 14 bytes freed), stop before 'new'.
	stdout, stderr, err := runEvictOpts(c, func(o *evictOptions) {
		o.TargetSizeSet = true
		o.TargetSize = 10
	})
	if err != nil {
		t.Fatalf("runEvict: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("expected 2 evicted lines, got %d: %q", len(lines), stdout)
	}
	if linesContain(lines, "new ") {
		t.Fatal("'new' should not be evicted yet")
	}
	if !strings.Contains(stderr, "evicted 2 entries") {
		t.Fatalf("expected stderr summary, got %q", stderr)
	}
	// Verify storage state.
	for _, k := range []string{"old", "mid"} {
		exists, _ := c.Exists(ctx, k)
		if exists {
			t.Fatalf("%s should be gone", k)
		}
	}
	exists, _ := c.Exists(ctx, "new")
	if !exists {
		t.Fatal("'new' should remain")
	}
}

func TestEvict_MinAgeProtectsFreshEntries(t *testing.T) {
	c, _ := newTestCacheWithIndex(t)
	idx := c.Index()
	ctx := context.Background()
	now := time.Now().Unix()

	old := index.Entry{Key: "old", Method: "GET", URL: "u1", Status: 200, BodySize: 1000, CreatedAt: 1, LastAccessedAt: now - 86400*7} // 7 days ago
	fresh := index.Entry{Key: "fresh", Method: "GET", URL: "u2", Status: 200, BodySize: 1000, CreatedAt: 1, LastAccessedAt: now}       // just now

	c.Put(ctx, "old", &cache.EntryMeta{Method: "GET", URL: "u1", StatusCode: 200}, bytes.NewReader(make([]byte, 1000)))
	c.Put(ctx, "fresh", &cache.EntryMeta{Method: "GET", URL: "u2", StatusCode: 200}, bytes.NewReader(make([]byte, 1000)))
	idx.Insert(ctx, old)
	idx.Insert(ctx, fresh)

	// Need to free 1500 bytes (total 2000, target 500). Without min-age,
	// both candidates are eligible. With --min-age 1d, fresh is protected
	// even though we don't reach target.
	_, stderr, err := runEvictOpts(c, func(o *evictOptions) {
		o.TargetSizeSet = true
		o.TargetSize = 500
		o.MinAge = 24 * time.Hour
	})
	if err != nil {
		t.Fatalf("runEvict: %v", err)
	}
	exists, _ := c.Exists(ctx, "fresh")
	if !exists {
		t.Fatal("fresh should be protected by min-age")
	}
	exists, _ = c.Exists(ctx, "old")
	if exists {
		t.Fatal("old should be evicted")
	}
	if !strings.Contains(stderr, "did not reach target") {
		t.Fatalf("expected stderr to warn about target miss, got %q", stderr)
	}
}

func TestEvict_DryRunLeavesStorage(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("k1", "GET", "https://x")
	put("k2", "GET", "https://y")

	_, stderr, err := runEvictOpts(c, func(o *evictOptions) {
		o.TargetSizeSet = true
		o.TargetSize = 0
		o.DryRun = true
	})
	if err != nil {
		t.Fatalf("runEvict: %v", err)
	}
	for _, k := range []string{"k1", "k2"} {
		exists, _ := c.Exists(context.Background(), k)
		if !exists {
			t.Fatalf("%s should remain after --dry-run", k)
		}
	}
	if !strings.Contains(stderr, "would evict") {
		t.Fatalf("expected 'would evict' in stderr, got %q", stderr)
	}
}

func TestEvict_JSON(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("k1", "GET", "https://x") // body = "body-k1" = 7

	stdout, _, err := runEvictOpts(c, func(o *evictOptions) {
		o.TargetSizeSet = true
		o.TargetSize = 0
		o.JSON = true
	})
	if err != nil {
		t.Fatalf("runEvict: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("invalid JSON: %v (%q)", err, stdout)
	}
	for _, f := range []string{"key", "method", "url", "body_size"} {
		if _, ok := got[f]; !ok {
			t.Fatalf("missing field %q: %v", f, got)
		}
	}
}

// ---- reindex ----

func runReindexOpts(c *cache.Cache, mutate func(*reindexOptions)) (string, string, error) {
	var stdout, stderr bytes.Buffer
	opts := reindexOptions{Cache: c, Stdout: &stdout, Stderr: &stderr}
	if mutate != nil {
		mutate(&opts)
	}
	err := runReindex(context.Background(), opts)
	return stdout.String(), stderr.String(), err
}

func TestReindex_NoIndexErrors(t *testing.T) {
	c, _ := newTestCache(t)
	_, _, err := runReindexOpts(c, nil)
	if err == nil || !strings.Contains(err.Error(), "index") {
		t.Fatalf("expected index error, got %v", err)
	}
}

func TestReindex_DryRunReports(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("ondisk", "GET", "u1") // both on disk and indexed → updated

	// Inject an orphan row.
	c.Index().Insert(context.Background(), index.Entry{Key: "ghost", Method: "GET", URL: "u2", Status: 200, BodySize: 1, CreatedAt: 1, LastAccessedAt: 1})

	_, stderr, err := runReindexOpts(c, func(o *reindexOptions) { o.DryRun = true })
	if err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if !strings.Contains(stderr, "updated=1") || !strings.Contains(stderr, "removed=1") {
		t.Fatalf("expected counts in stderr, got %q", stderr)
	}
	// Index unchanged after dry-run.
	if _, err := c.Index().Get(context.Background(), "ghost"); err != nil {
		t.Fatal("dry-run should not have removed ghost")
	}
}

func TestReindex_AppliesChanges(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("ondisk", "GET", "u1")
	c.Index().Insert(context.Background(), index.Entry{Key: "ghost", Method: "GET", URL: "u2", Status: 200, BodySize: 1, CreatedAt: 1, LastAccessedAt: 1})

	_, stderr, err := runReindexOpts(c, nil)
	if err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if !strings.Contains(stderr, "removed=1") {
		t.Fatalf("expected removed=1, got %q", stderr)
	}
	if _, err := c.Index().Get(context.Background(), "ghost"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ghost should be gone, got %v", err)
	}
}

// ---- domains ----

func runDomainsOpts(c *cache.Cache, mutate func(*domainsOptions)) (string, string, error) {
	var stdout, stderr bytes.Buffer
	opts := domainsOptions{Cache: c, Stdout: &stdout, Stderr: &stderr}
	if mutate != nil {
		mutate(&opts)
	}
	err := runDomains(context.Background(), opts)
	return stdout.String(), stderr.String(), err
}

func TestDomains_DistinctSorted(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://example.com/b")
	put("k3", "GET", "https://registry.npmjs.org/pkg")
	put("k4", "GET", "https://files.pythonhosted.org/x")

	stdout, stderr, err := runDomainsOpts(c, nil)
	if err != nil {
		t.Fatalf("runDomains: %v", err)
	}
	got := nonEmptyLines(stdout)
	want := []string{"example.com", "files.pythonhosted.org", "registry.npmjs.org"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: expected %q, got %q (all=%v)", i, want[i], got[i], got)
		}
	}
	if !strings.Contains(stderr, "3 domains") {
		t.Fatalf("expected summary, got %q", stderr)
	}
}

func TestDomains_Count(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://example.com/b")
	put("k3", "GET", "https://registry.npmjs.org/pkg")

	stdout, _, err := runDomainsOpts(c, func(o *domainsOptions) { o.Count = true })
	if err != nil {
		t.Fatalf("runDomains: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if !linesContain(lines, "2 example.com") {
		t.Fatalf("expected '2 example.com', got %v", lines)
	}
	if !linesContain(lines, "1 registry.npmjs.org") {
		t.Fatalf("expected '1 registry.npmjs.org', got %v", lines)
	}
}

func TestDomains_JSON(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://example.com/b")

	stdout, _, err := runDomainsOpts(c, func(o *domainsOptions) { o.JSON = true })
	if err != nil {
		t.Fatalf("runDomains: %v", err)
	}
	var got domainRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("invalid JSON: %v (%q)", err, stdout)
	}
	if got.Domain != "example.com" || got.Count != 2 {
		t.Fatalf("unexpected row: %+v", got)
	}
}

func TestDomains_PortPreserved(t *testing.T) {
	c, put := newTestCache(t)
	put("k1", "GET", "https://example.com:8443/a")

	stdout, _, err := runDomainsOpts(c, nil)
	if err != nil {
		t.Fatalf("runDomains: %v", err)
	}
	if !linesContain(nonEmptyLines(stdout), "example.com:8443") {
		t.Fatalf("expected host:port preserved, got %q", stdout)
	}
}

func TestDomains_UsesIndex(t *testing.T) {
	c, put := newTestCacheWithIndex(t)
	put("k1", "GET", "https://example.com/a")
	put("k2", "GET", "https://registry.npmjs.org/pkg")

	stdout, stderr, err := runDomainsOpts(c, nil)
	if err != nil {
		t.Fatalf("runDomains: %v", err)
	}
	lines := nonEmptyLines(stdout)
	if !linesContain(lines, "example.com") || !linesContain(lines, "registry.npmjs.org") {
		t.Fatalf("expected both domains, got %v", lines)
	}
	if !strings.Contains(stderr, "2 domains") {
		t.Fatalf("expected summary, got %q", stderr)
	}
}

func TestDomains_Empty(t *testing.T) {
	c, _ := newTestCache(t)
	stdout, stderr, err := runDomainsOpts(c, nil)
	if err != nil {
		t.Fatalf("runDomains: %v", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "0 domains") {
		t.Fatalf("expected '0 domains', got %q", stderr)
	}
}

// ---- helpers ----

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func linesContain(lines []string, needle string) bool {
	for _, l := range lines {
		if strings.Contains(l, needle) {
			return true
		}
	}
	return false
}
