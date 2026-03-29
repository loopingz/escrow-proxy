package cache

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

type Cache struct {
	storage storage.Storage
}

func New(s storage.Storage) *Cache {
	return &Cache{storage: s}
}

func metaKey(key string) string { return key + ".meta" }
func bodyKey(key string) string { return key + ".body" }

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
