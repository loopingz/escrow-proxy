package archive

import (
	"context"
	"io"
)

type Writer interface {
	Add(ctx context.Context, key string, meta []byte, body io.Reader) error
	Close() error
}

type Reader interface {
	Get(ctx context.Context, key string) ([]byte, io.ReadCloser, error)
	List(ctx context.Context) ([]string, error)
	Close() error
}

type Format interface {
	NewWriter(dest string) (Writer, error)
	NewReader(src string) (Reader, error)
}
