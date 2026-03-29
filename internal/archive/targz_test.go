package archive_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestTarGz_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.tar.gz")
	ctx := context.Background()

	format := &archive.TarGzFormat{}

	w, err := format.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	entries := map[string]struct{ meta, body string }{
		"key1": {meta: `{"url":"https://example.com/a"}`, body: "body-a"},
		"key2": {meta: `{"url":"https://example.com/b"}`, body: "body-b"},
	}

	for k, v := range entries {
		if err := w.Add(ctx, k, []byte(v.meta), bytes.NewReader([]byte(v.body))); err != nil {
			t.Fatalf("Add %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("archive file not found: %v", err)
	}

	r, err := format.NewReader(path)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	keys, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "key1" || keys[1] != "key2" {
		t.Fatalf("List: got %v, want [key1 key2]", keys)
	}

	for k, want := range entries {
		meta, bodyRC, err := r.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		body, _ := io.ReadAll(bodyRC)
		bodyRC.Close()

		if string(meta) != want.meta {
			t.Fatalf("Get %s meta: got %q, want %q", k, meta, want.meta)
		}
		if string(body) != want.body {
			t.Fatalf("Get %s body: got %q, want %q", k, body, want.body)
		}
	}
}

func TestTarGz_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.tar.gz")
	ctx := context.Background()

	format := &archive.TarGzFormat{}
	w, _ := format.NewWriter(path)
	w.Add(ctx, "key1", []byte("meta"), bytes.NewReader([]byte("body")))
	w.Close()

	r, _ := format.NewReader(path)
	defer r.Close()

	_, _, err := r.Get(ctx, "missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}
