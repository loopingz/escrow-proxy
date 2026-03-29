package cache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/loopingz/escrow-proxy/internal/archive"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// ArchiveStorage wraps an archive.Reader as a storage.Storage for offline mode.
type ArchiveStorage struct {
	reader archive.Reader
}

// NewArchiveStorage creates a read-only storage backed by an archive reader.
func NewArchiveStorage(r archive.Reader) *ArchiveStorage {
	return &ArchiveStorage{reader: r}
}

func (s *ArchiveStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	var baseKey, suffix string
	if strings.HasSuffix(key, ".meta") {
		baseKey = key[:len(key)-5]
		suffix = "meta"
	} else if strings.HasSuffix(key, ".body") {
		baseKey = key[:len(key)-5]
		suffix = "body"
	} else {
		return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}

	meta, body, err := s.reader.Get(ctx, baseKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}

	if suffix == "meta" {
		body.Close()
		return io.NopCloser(bytes.NewReader(meta)), nil
	}
	return body, nil
}

func (s *ArchiveStorage) Put(_ context.Context, _ string, _ io.Reader) error {
	return fmt.Errorf("archive storage is read-only")
}

func (s *ArchiveStorage) Exists(ctx context.Context, key string) (bool, error) {
	rc, err := s.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	rc.Close()
	return true, nil
}

func (s *ArchiveStorage) Delete(_ context.Context, _ string) error {
	return fmt.Errorf("archive storage is read-only")
}

func (s *ArchiveStorage) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.reader.List(ctx)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			result = append(result, k+".meta", k+".body")
		}
	}
	return result, nil
}
