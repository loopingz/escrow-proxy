package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// TarGzFormat implements Format for tar.gz archives.
type TarGzFormat struct{}

func (f *TarGzFormat) NewWriter(dest string) (Writer, error) {
	file, err := os.Create(dest)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	gw := gzip.NewWriter(file)
	tw := tar.NewWriter(gw)
	return &tarGzWriter{
		file: file,
		gw:   gw,
		tw:   tw,
		keys: []string{},
	}, nil
}

func (f *TarGzFormat) NewReader(src string) (Reader, error) {
	file, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	gr, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	metas := make(map[string][]byte)
	bodies := make(map[string][]byte)
	var index []string

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read entry %s: %w", hdr.Name, err)
		}

		switch {
		case hdr.Name == "index.json":
			if err := json.Unmarshal(data, &index); err != nil {
				return nil, fmt.Errorf("parse index.json: %w", err)
			}
		case strings.HasSuffix(hdr.Name, ".meta"):
			key := strings.TrimSuffix(hdr.Name, ".meta")
			metas[key] = data
		case strings.HasSuffix(hdr.Name, ".body"):
			key := strings.TrimSuffix(hdr.Name, ".body")
			bodies[key] = data
		}
	}

	return &tarGzReader{
		metas:  metas,
		bodies: bodies,
		index:  index,
	}, nil
}

// tarGzWriter implements Writer for tar.gz archives.
type tarGzWriter struct {
	file *os.File
	gw   *gzip.Writer
	tw   *tar.Writer
	keys []string
}

func (w *tarGzWriter) Add(ctx context.Context, key string, meta []byte, body io.Reader) error {
	// Write meta entry
	if err := w.writeEntry(key+".meta", meta); err != nil {
		return fmt.Errorf("write meta for %s: %w", key, err)
	}

	// Read body into buffer to get size
	bodyData, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read body for %s: %w", key, err)
	}

	// Write body entry
	if err := w.writeEntry(key+".body", bodyData); err != nil {
		return fmt.Errorf("write body for %s: %w", key, err)
	}

	w.keys = append(w.keys, key)
	return nil
}

func (w *tarGzWriter) writeEntry(name string, data []byte) error {
	hdr := &tar.Header{
		Name: name,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := w.tw.Write(data)
	return err
}

func (w *tarGzWriter) Close() error {
	// Write index.json
	indexData, err := json.Marshal(w.keys)
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	if err := w.writeEntry("index.json", indexData); err != nil {
		return fmt.Errorf("write index.json: %w", err)
	}

	if err := w.tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if err := w.gw.Close(); err != nil {
		return fmt.Errorf("close gzip: %w", err)
	}
	return w.file.Close()
}

// tarGzReader implements Reader backed by in-memory maps.
type tarGzReader struct {
	metas  map[string][]byte
	bodies map[string][]byte
	index  []string
}

func (r *tarGzReader) Get(ctx context.Context, key string) ([]byte, io.ReadCloser, error) {
	meta, ok := r.metas[key]
	if !ok {
		return nil, nil, fmt.Errorf("key not found: %s", key)
	}
	body := r.bodies[key]
	return meta, io.NopCloser(bytes.NewReader(body)), nil
}

func (r *tarGzReader) List(ctx context.Context) ([]string, error) {
	return r.index, nil
}

func (r *tarGzReader) Close() error {
	return nil
}
