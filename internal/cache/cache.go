package cache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

type Cache struct {
	storage storage.Storage
}

func New(s storage.Storage) *Cache {
	return &Cache{storage: s}
}

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

	return nil
}

func (c *Cache) Get(ctx context.Context, key string) (*EntryMeta, io.ReadCloser, error) {
	metaRC, err := c.storage.Get(ctx, metaKey(key))
	if err != nil {
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
		return nil, nil, fmt.Errorf("reading body: %w", err)
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
		return fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	if err := c.storage.Delete(ctx, metaKey(key)); err != nil {
		return fmt.Errorf("deleting meta: %w", err)
	}
	if err := c.storage.Delete(ctx, bodyKey(key)); err != nil {
		return fmt.Errorf("deleting body: %w", err)
	}
	return nil
}
