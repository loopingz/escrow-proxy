package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

type Tiered struct {
	tiers []Storage
}

func NewTiered(tiers []Storage) *Tiered {
	return &Tiered{tiers: tiers}
}

func (t *Tiered) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	for i, tier := range t.tiers {
		rc, err := tier.Get(ctx, key)
		if err != nil {
			continue
		}
		if i == 0 {
			return rc, nil
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading from tier %d: %w", i, err)
		}
		for j := 0; j < i; j++ {
			_ = t.tiers[j].Put(ctx, key, bytes.NewReader(data))
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
}

func (t *Tiered) Put(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading data: %w", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, len(t.tiers))
	for i, tier := range t.tiers {
		wg.Add(1)
		go func(idx int, s Storage) {
			defer wg.Done()
			errs[idx] = s.Put(ctx, key, bytes.NewReader(data))
		}(i, tier)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("tier %d put: %w", i, err)
		}
	}
	return nil
}

func (t *Tiered) Exists(ctx context.Context, key string) (bool, error) {
	for _, tier := range t.tiers {
		exists, err := tier.Exists(ctx, key)
		if err != nil {
			continue
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

func (t *Tiered) Delete(ctx context.Context, key string) error {
	var wg sync.WaitGroup
	errs := make([]error, len(t.tiers))
	for i, tier := range t.tiers {
		wg.Add(1)
		go func(idx int, s Storage) {
			defer wg.Done()
			errs[idx] = s.Delete(ctx, key)
		}(i, tier)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("tier %d delete: %w", i, err)
		}
	}
	return nil
}

func (t *Tiered) List(ctx context.Context, prefix string) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string
	for _, tier := range t.tiers {
		keys, err := tier.List(ctx, prefix)
		if err != nil {
			continue
		}
		for _, k := range keys {
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				result = append(result, k)
			}
		}
	}
	return result, nil
}
