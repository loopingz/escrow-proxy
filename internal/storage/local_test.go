package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestLocalStorage_PutAndGet(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	data := []byte("hello world")
	if err := s.Put(ctx, "test-key", bytes.NewReader(data)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, "test-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q, want %q", got, data)
	}
}

func TestLocalStorage_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestLocalStorage_Exists(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	exists, err := s.Exists(ctx, "missing")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("expected false for missing key")
	}

	if err := s.Put(ctx, "present", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err = s.Exists(ctx, "present")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected true for present key")
	}
}

func TestLocalStorage_Delete(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	if err := s.Put(ctx, "to-delete", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, err := s.Exists(ctx, "to-delete")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("expected key to be deleted")
	}
}

func TestLocalStorage_List(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	keys := []string{"abc123.meta", "abc123.body", "def456.meta", "def456.body"}
	for _, k := range keys {
		if err := s.Put(ctx, k, bytes.NewReader([]byte("data"))); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	got, err := s.List(ctx, "abc")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 keys with prefix 'abc', got %d: %v", len(got), got)
	}

	all, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 keys total, got %d", len(all))
	}
}
