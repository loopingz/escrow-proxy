package archive_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestOCI_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci")
	ctx := context.Background()

	format := &archive.OCIFormat{EntriesPerLayer: 2}

	w, err := format.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	entries := map[string]struct{ meta, body string }{
		"key1": {meta: `{"url":"https://example.com/a"}`, body: "body-a"},
		"key2": {meta: `{"url":"https://example.com/b"}`, body: "body-b"},
		"key3": {meta: `{"url":"https://example.com/c"}`, body: "body-c"},
	}

	for k, v := range entries {
		if err := w.Add(ctx, k, []byte(v.meta), bytes.NewReader([]byte(v.body))); err != nil {
			t.Fatalf("Add %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	r, err := format.NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	keys, _ := r.List(ctx)
	sort.Strings(keys)
	if len(keys) != 3 {
		t.Fatalf("List: got %d keys, want 3", len(keys))
	}

	for k, want := range entries {
		meta, bodyRC, err := r.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		body, _ := io.ReadAll(bodyRC)
		bodyRC.Close()
		if string(meta) != want.meta {
			t.Fatalf("meta: got %q, want %q", meta, want.meta)
		}
		if string(body) != want.body {
			t.Fatalf("body: got %q, want %q", body, want.body)
		}
	}
}

func TestOCI_LayerGrouping(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci")
	ctx := context.Background()

	format := &archive.OCIFormat{EntriesPerLayer: 2}
	w, _ := format.NewWriter(dir)

	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key%d", i)
		w.Add(ctx, key, []byte(`{"url":"test"}`), bytes.NewReader([]byte("body")))
	}
	w.Close()

	r, _ := format.NewReader(dir)
	defer r.Close()

	keys, _ := r.List(ctx)
	if len(keys) != 5 {
		t.Fatalf("expected 5 keys, got %d", len(keys))
	}
}
