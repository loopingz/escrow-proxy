package storage_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestTieredStorage_GetPromotesToL1(t *testing.T) {
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	l1 := storage.NewLocal(l1Dir)
	l2 := storage.NewLocal(l2Dir)
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	if err := l2.Put(ctx, "key1", bytes.NewReader([]byte("from-l2"))); err != nil {
		t.Fatalf("L2 Put: %v", err)
	}

	rc, err := tiered.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Tiered Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "from-l2" {
		t.Fatalf("got %q, want %q", got, "from-l2")
	}

	exists, err := l1.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("L1 Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected key to be promoted to L1")
	}
}

func TestTieredStorage_PutWritesToAllTiers(t *testing.T) {
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	l1 := storage.NewLocal(l1Dir)
	l2 := storage.NewLocal(l2Dir)
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	if err := tiered.Put(ctx, "key1", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Tiered Put: %v", err)
	}

	for i, s := range []storage.Storage{l1, l2} {
		exists, err := s.Exists(ctx, "key1")
		if err != nil {
			t.Fatalf("tier %d Exists: %v", i, err)
		}
		if !exists {
			t.Fatalf("expected key in tier %d", i)
		}
	}
}

func TestTieredStorage_GetNotFound(t *testing.T) {
	l1 := storage.NewLocal(t.TempDir())
	l2 := storage.NewLocal(t.TempDir())
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	_, err := tiered.Get(ctx, "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTieredStorage_DeleteFromAllTiers(t *testing.T) {
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	l1 := storage.NewLocal(l1Dir)
	l2 := storage.NewLocal(l2Dir)
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	if err := tiered.Put(ctx, "key1", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := tiered.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for i, s := range []storage.Storage{l1, l2} {
		exists, _ := s.Exists(ctx, "key1")
		if exists {
			t.Fatalf("expected key deleted from tier %d", i)
		}
	}
}

func TestTieredStorage_ExistsReturnsOnFirstHit(t *testing.T) {
	l1 := storage.NewLocal(t.TempDir())
	l2 := storage.NewLocal(t.TempDir())
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	if err := l1.Put(ctx, "key1", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("L1 Put: %v", err)
	}

	exists, err := tiered.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected true")
	}
}
