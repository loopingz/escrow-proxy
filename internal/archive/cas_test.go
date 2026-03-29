package archive_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestCAS_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cas")
	ctx := context.Background()
	format := &archive.CASFormat{}

	w, err := format.NewWriter(dir)
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

	r, err := format.NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	keys, _ := r.List(ctx)
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "key1" || keys[1] != "key2" {
		t.Fatalf("List: got %v", keys)
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

func TestCAS_DedupesIdenticalBodies(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cas")
	ctx := context.Background()
	format := &archive.CASFormat{}
	w, _ := format.NewWriter(dir)

	sameBody := "identical-content"
	w.Add(ctx, "key1", []byte(`{"url":"a"}`), bytes.NewReader([]byte(sameBody)))
	w.Add(ctx, "key2", []byte(`{"url":"b"}`), bytes.NewReader([]byte(sameBody)))
	w.Close()

	r, _ := format.NewReader(dir)
	defer r.Close()

	_, b1, _ := r.Get(ctx, "key1")
	body1, _ := io.ReadAll(b1)
	b1.Close()
	_, b2, _ := r.Get(ctx, "key2")
	body2, _ := io.ReadAll(b2)
	b2.Close()

	if string(body1) != sameBody || string(body2) != sameBody {
		t.Fatal("expected identical bodies")
	}
}
