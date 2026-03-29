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

func TestS3_PutAndGet(t *testing.T) {
	bucket := os.Getenv("TEST_S3_BUCKET")
	region := os.Getenv("TEST_S3_REGION")
	if bucket == "" || region == "" {
		t.Skip("TEST_S3_BUCKET or TEST_S3_REGION not set")
	}

	s, err := storage.NewS3(context.Background(), bucket, "test-prefix/", region)
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	ctx := context.Background()

	data := []byte("hello s3")
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
