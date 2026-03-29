package cache

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/loopingz/escrow-proxy/internal/archive"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// Recorder wraps a Cache and records all entries for archiving.
type Recorder struct {
	cache  *Cache
	writer archive.Writer
	mu     sync.Mutex
	keys   []string
}

// NewRecorder creates a Recorder that tracks cache writes for later archiving.
func NewRecorder(c *Cache, w archive.Writer) *Recorder {
	return &Recorder{cache: c, writer: w}
}

// Cache returns a wrapped Cache whose writes are tracked by the Recorder.
func (r *Recorder) Cache() *Cache {
	return &Cache{storage: &recordingStorage{
		inner:    r.cache.storage,
		recorder: r,
	}}
}

func (r *Recorder) recordEntry(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
}

// Finalize reads all recorded entries from the underlying storage and writes
// them into the archive, then closes the archive writer.
func (r *Recorder) Finalize() error {
	r.mu.Lock()
	keys := make([]string, len(r.keys))
	copy(keys, r.keys)
	r.mu.Unlock()

	ctx := context.Background()
	seen := make(map[string]bool)
	for _, key := range keys {
		baseKey := key
		if strings.HasSuffix(key, ".meta") {
			baseKey = key[:len(key)-5]
		} else if strings.HasSuffix(key, ".body") {
			baseKey = key[:len(key)-5]
		}
		if seen[baseKey] {
			continue
		}
		seen[baseKey] = true

		metaRC, err := r.cache.storage.Get(ctx, metaKey(baseKey))
		if err != nil {
			continue
		}
		metaBytes, _ := io.ReadAll(metaRC)
		metaRC.Close()

		bodyRC, err := r.cache.storage.Get(ctx, bodyKey(baseKey))
		if err != nil {
			continue
		}

		if err := r.writer.Add(ctx, baseKey, metaBytes, bodyRC); err != nil {
			bodyRC.Close()
			return fmt.Errorf("adding %s to archive: %w", baseKey, err)
		}
		bodyRC.Close()
	}

	return r.writer.Close()
}

// recordingStorage is a storage.Storage wrapper that records Put keys.
type recordingStorage struct {
	inner    storage.Storage
	recorder *Recorder
}

func (s *recordingStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.inner.Get(ctx, key)
}

func (s *recordingStorage) Put(ctx context.Context, key string, r io.Reader) error {
	err := s.inner.Put(ctx, key, r)
	if err == nil {
		s.recorder.recordEntry(key)
	}
	return err
}

func (s *recordingStorage) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *recordingStorage) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *recordingStorage) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}
