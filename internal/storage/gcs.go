package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	gcsstorage "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

type GCS struct {
	client *gcsstorage.Client
	bucket string
	prefix string
}

func NewGCS(ctx context.Context, bucket, prefix string) (*GCS, error) {
	client, err := gcsstorage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating GCS client: %w", err)
	}
	return &GCS{client: client, bucket: bucket, prefix: prefix}, nil
}

func (g *GCS) objectName(key string) string {
	return g.prefix + key
}

func (g *GCS) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := g.client.Bucket(g.bucket).Object(g.objectName(key)).NewReader(ctx)
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("reading %s from GCS: %w", key, err)
	}
	return rc, nil
}

func (g *GCS) Put(ctx context.Context, key string, r io.Reader) error {
	w := g.client.Bucket(g.bucket).Object(g.objectName(key)).NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return fmt.Errorf("writing %s to GCS: %w", key, err)
	}
	return w.Close()
}

func (g *GCS) Exists(ctx context.Context, key string) (bool, error) {
	_, err := g.client.Bucket(g.bucket).Object(g.objectName(key)).Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s in GCS: %w", key, err)
	}
	return true, nil
}

func (g *GCS) Delete(ctx context.Context, key string) error {
	err := g.client.Bucket(g.bucket).Object(g.objectName(key)).Delete(ctx)
	if err != nil && !errors.Is(err, gcsstorage.ErrObjectNotExist) {
		return fmt.Errorf("deleting %s from GCS: %w", key, err)
	}
	return nil
}

func (g *GCS) List(ctx context.Context, prefix string) ([]string, error) {
	it := g.client.Bucket(g.bucket).Objects(ctx, &gcsstorage.Query{
		Prefix: g.objectName(prefix),
	})
	var keys []string
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing GCS objects: %w", err)
		}
		key := strings.TrimPrefix(attrs.Name, g.prefix)
		keys = append(keys, key)
	}
	return keys, nil
}
