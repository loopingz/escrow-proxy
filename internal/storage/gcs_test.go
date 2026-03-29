//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestGCS_PutAndGet(t *testing.T) {
	bucket := os.Getenv("TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("TEST_GCS_BUCKET not set")
	}

	s, err := storage.NewGCS(context.Background(), bucket, "test-prefix/")
	if err != nil {
		t.Fatalf("NewGCS: %v", err)
	}
	ctx := context.Background()

	data := []byte("hello gcs")
	if err := s.Put(ctx, "test-key", bytes.NewReader(data)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	defer s.Delete(ctx, "test-key")

	rc, err := s.Get(ctx, "test-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q, want %q", got, data)
	}
}
