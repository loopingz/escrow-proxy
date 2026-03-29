package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CASFormat implements the Format interface for a content-addressed storage layout:
//
//	cas/
//	├── index.json          # key → {meta_digest, body_digest}
//	├── blobs/sha256/...    # body blobs keyed by SHA256 hex digest
//	└── meta/{key}.json     # metadata files
type CASFormat struct{}

func (f *CASFormat) NewWriter(dest string) (Writer, error) {
	for _, sub := range []string{"blobs/sha256", "meta"} {
		if err := os.MkdirAll(filepath.Join(dest, sub), 0o755); err != nil {
			return nil, fmt.Errorf("cas mkdir %s: %w", sub, err)
		}
	}
	return &casWriter{
		root:  dest,
		index: make(map[string]casIndexEntry),
	}, nil
}

func (f *CASFormat) NewReader(src string) (Reader, error) {
	data, err := os.ReadFile(filepath.Join(src, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("cas read index: %w", err)
	}
	var index map[string]casIndexEntry
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("cas parse index: %w", err)
	}
	return &casReader{root: src, index: index}, nil
}

// casIndexEntry maps a key to its metadata and body digests.
type casIndexEntry struct {
	MetaDigest string `json:"meta_digest"`
	BodyDigest string `json:"body_digest"`
}

// casWriter writes entries into the CAS layout.
type casWriter struct {
	root  string
	index map[string]casIndexEntry
}

func (w *casWriter) Add(ctx context.Context, key string, meta []byte, body io.Reader) error {
	// Hash and write body blob (deduped by checking if file already exists).
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("cas read body for %s: %w", key, err)
	}
	bodySum := sha256.Sum256(bodyBytes)
	bodyDigest := hex.EncodeToString(bodySum[:])
	blobPath := filepath.Join(w.root, "blobs", "sha256", bodyDigest)
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		if err := os.WriteFile(blobPath, bodyBytes, 0o644); err != nil {
			return fmt.Errorf("cas write blob %s: %w", bodyDigest, err)
		}
	}

	// Hash and write metadata file.
	metaSum := sha256.Sum256(meta)
	metaDigest := hex.EncodeToString(metaSum[:])
	metaPath := filepath.Join(w.root, "meta", key+".json")
	if err := os.WriteFile(metaPath, meta, 0o644); err != nil {
		return fmt.Errorf("cas write meta %s: %w", key, err)
	}

	w.index[key] = casIndexEntry{
		MetaDigest: metaDigest,
		BodyDigest: bodyDigest,
	}
	return nil
}

func (w *casWriter) Close() error {
	data, err := json.MarshalIndent(w.index, "", "  ")
	if err != nil {
		return fmt.Errorf("cas marshal index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(w.root, "index.json"), data, 0o644); err != nil {
		return fmt.Errorf("cas write index: %w", err)
	}
	return nil
}

// casReader reads entries from a CAS layout.
type casReader struct {
	root  string
	index map[string]casIndexEntry
}

func (r *casReader) Get(ctx context.Context, key string) ([]byte, io.ReadCloser, error) {
	entry, ok := r.index[key]
	if !ok {
		return nil, nil, fmt.Errorf("cas: key %q not found", key)
	}

	meta, err := os.ReadFile(filepath.Join(r.root, "meta", key+".json"))
	if err != nil {
		return nil, nil, fmt.Errorf("cas read meta %s: %w", key, err)
	}

	blobPath := filepath.Join(r.root, "blobs", "sha256", entry.BodyDigest)
	f, err := os.Open(blobPath)
	if err != nil {
		return nil, nil, fmt.Errorf("cas open blob %s: %w", entry.BodyDigest, err)
	}

	return meta, f, nil
}

func (r *casReader) List(ctx context.Context) ([]string, error) {
	keys := make([]string, 0, len(r.index))
	for k := range r.index {
		keys = append(keys, k)
	}
	return keys, nil
}

func (r *casReader) Close() error {
	return nil
}
