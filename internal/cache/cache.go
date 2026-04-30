package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/loopingz/escrow-proxy/internal/index"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

type Cache struct {
	storage storage.Storage
	index   *index.Index // optional; nil means no SQLite index
}

func New(s storage.Storage) *Cache {
	return &Cache{storage: s}
}

// WithIndex attaches an Index for fast queries and eviction. The Index is
// optional; if nil, the Cache behaves exactly as it did before.
func (c *Cache) WithIndex(idx *index.Index) *Cache {
	c.index = idx
	return c
}

// Index returns the attached Index, or nil. Callers (CLI evict/reindex)
// use this to read the index directly.
func (c *Cache) Index() *index.Index { return c.index }

const (
	metaSuffix = ".meta"
	bodySuffix = ".body"
)

func metaKey(key string) string { return key + metaSuffix }
func bodyKey(key string) string { return key + bodySuffix }

func (c *Cache) Put(ctx context.Context, key string, meta *EntryMeta, body io.Reader) error {
	metaBytes, err := MarshalMeta(meta)
	if err != nil {
		return fmt.Errorf("marshaling meta: %w", err)
	}

	if err := c.storage.Put(ctx, metaKey(key), bytes.NewReader(metaBytes)); err != nil {
		return fmt.Errorf("storing meta: %w", err)
	}

	if err := c.storage.Put(ctx, bodyKey(key), body); err != nil {
		return fmt.Errorf("storing body: %w", err)
	}

	if c.index != nil {
		size, err := c.storage.Size(ctx, bodyKey(key))
		if err != nil {
			// Storage just succeeded a Put — failing Size here is unusual
			// but don't poison the put. Log via returned error if the
			// index insert fails downstream; here we just default to 0.
			size = 0
		}
		now := time.Now().Unix()
		_ = c.index.Insert(ctx, index.Entry{
			Key:            key,
			Method:         meta.Method,
			URL:            meta.URL,
			Status:         meta.StatusCode,
			BodySize:       size,
			CreatedAt:      now,
			LastAccessedAt: now,
			HitCount:       0,
		})
	}

	return nil
}

func (c *Cache) Get(ctx context.Context, key string) (*EntryMeta, io.ReadCloser, error) {
	metaRC, err := c.storage.Get(ctx, metaKey(key))
	if err != nil {
		// Lazy reconcile: storage missing, drop any orphan index row.
		if c.index != nil && errors.Is(err, storage.ErrNotFound) {
			_ = c.index.Delete(ctx, key)
		}
		return nil, nil, fmt.Errorf("reading meta: %w", err)
	}
	metaBytes, err := io.ReadAll(metaRC)
	metaRC.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("reading meta bytes: %w", err)
	}

	meta, err := UnmarshalMeta(metaBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("unmarshaling meta: %w", err)
	}

	bodyRC, err := c.storage.Get(ctx, bodyKey(key))
	if err != nil {
		if c.index != nil && errors.Is(err, storage.ErrNotFound) {
			_ = c.index.Delete(ctx, key)
		}
		return nil, nil, fmt.Errorf("reading body: %w", err)
	}

	if c.index != nil {
		size, sizeErr := c.storage.Size(ctx, bodyKey(key))
		if sizeErr != nil {
			size = 0
		}
		c.index.RecordHit(index.Entry{
			Key:      key,
			Method:   meta.Method,
			URL:      meta.URL,
			Status:   meta.StatusCode,
			BodySize: size,
		})
	}

	return meta, bodyRC, nil
}

func (c *Cache) Exists(ctx context.Context, key string) (bool, error) {
	return c.storage.Exists(ctx, metaKey(key))
}

func (c *Cache) Size(ctx context.Context, key string) (int64, error) {
	return c.storage.Size(ctx, bodyKey(key))
}

func (c *Cache) Walk(ctx context.Context, fn func(key string, meta *EntryMeta) error) error {
	keys, err := c.storage.List(ctx, "")
	if err != nil {
		return fmt.Errorf("listing storage: %w", err)
	}
	for _, k := range keys {
		if !strings.HasSuffix(k, metaSuffix) {
			continue
		}
		cacheKey := strings.TrimSuffix(k, metaSuffix)

		rc, err := c.storage.Get(ctx, k)
		if err != nil {
			continue // skip unreadable entry
		}
		metaBytes, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			continue
		}
		meta, err := UnmarshalMeta(metaBytes)
		if err != nil {
			continue // skip corrupt entry
		}
		if err := fn(cacheKey, meta); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) Delete(ctx context.Context, key string) error {
	exists, err := c.storage.Exists(ctx, metaKey(key))
	if err != nil {
		return fmt.Errorf("checking meta: %w", err)
	}
	if !exists {
		if c.index != nil {
			_ = c.index.Delete(ctx, key) // best-effort orphan cleanup
		}
		return fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	if err := c.storage.Delete(ctx, metaKey(key)); err != nil {
		return fmt.Errorf("deleting meta: %w", err)
	}
	if err := c.storage.Delete(ctx, bodyKey(key)); err != nil {
		return fmt.Errorf("deleting body: %w", err)
	}
	if c.index != nil {
		_ = c.index.Delete(ctx, key)
	}
	return nil
}

// Reindex rebuilds the index from storage. Existing usage stats
// (created_at, last_accessed_at, hit_count) are preserved on already-
// indexed entries; meta and body_size are refreshed. Orphan index rows
// (in DB but not on disk) are deleted.
//
// Returns inserted, updated, removed counts.
func (c *Cache) Reindex(ctx context.Context) (inserted, updated, removed int, err error) {
	if c.index == nil {
		return 0, 0, 0, errors.New("no index attached")
	}

	// First, drain pending hits so existing rows are not stale.
	if err := c.index.Flush(ctx); err != nil {
		return 0, 0, 0, fmt.Errorf("flush before reindex: %w", err)
	}

	seen := make(map[string]bool)
	walkErr := c.Walk(ctx, func(key string, meta *EntryMeta) error {
		seen[key] = true

		size, sizeErr := c.storage.Size(ctx, bodyKey(key))
		if sizeErr != nil {
			size = 0
		}

		// Probe whether the row exists, so we can count insert vs update.
		_, getErr := c.index.Get(ctx, key)
		if errors.Is(getErr, storage.ErrNotFound) {
			inserted++
		} else if getErr == nil {
			updated++
		} else {
			return fmt.Errorf("probe %s: %w", key, getErr)
		}

		entry := index.Entry{
			Key:      key,
			Method:   meta.Method,
			URL:      meta.URL,
			Status:   meta.StatusCode,
			BodySize: size,
		}
		if err := c.index.Upsert(ctx, entry); err != nil {
			return fmt.Errorf("upsert %s: %w", key, err)
		}
		return nil
	})
	if walkErr != nil {
		return inserted, updated, removed, fmt.Errorf("walk: %w", walkErr)
	}

	// Delete orphan rows (in DB, not in storage).
	keys, err := c.index.AllKeys(ctx)
	if err != nil {
		return inserted, updated, removed, fmt.Errorf("listing index keys: %w", err)
	}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		if err := c.index.Delete(ctx, k); err != nil {
			return inserted, updated, removed, fmt.Errorf("delete orphan %s: %w", k, err)
		}
		removed++
	}
	return inserted, updated, removed, nil
}
