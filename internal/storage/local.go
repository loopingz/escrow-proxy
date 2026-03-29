package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Local struct {
	dir string
}

func NewLocal(dir string) *Local {
	return &Local{dir: dir}
}

func (l *Local) keyPath(key string) string {
	return filepath.Join(l.dir, key)
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(l.keyPath(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("opening %s: %w", key, err)
	}
	return f, nil
}

func (l *Local) Put(_ context.Context, key string, r io.Reader) error {
	path := l.keyPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", key, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("writing %s: %w", key, err)
	}
	return nil
}

func (l *Local) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(l.keyPath(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", key, err)
	}
	return true, nil
}

func (l *Local) Delete(_ context.Context, key string) error {
	err := os.Remove(l.keyPath(key))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", key, err)
	}
	return nil
}

func (l *Local) List(_ context.Context, prefix string) ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing directory: %w", err)
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if prefix == "" || strings.HasPrefix(e.Name(), prefix) {
			keys = append(keys, e.Name())
		}
	}
	return keys, nil
}
