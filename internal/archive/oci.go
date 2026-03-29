package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	ociLayoutVersion    = "1.0.0"
	defaultEntriesPerLayer = 1000
	mediaTypeOCILayout  = "application/vnd.oci.image.index.v1+json"
	mediaTypeManifest   = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeConfig     = "application/vnd.oci.image.config.v1+json"
	mediaTypeLayer      = "application/vnd.oci.image.layer.v1.tar"
)

// OCIFormat implements Format using the OCI image layout specification.
type OCIFormat struct {
	// EntriesPerLayer controls how many entries are grouped into each tar layer.
	// Defaults to 1000 if zero.
	EntriesPerLayer int
}

func (f *OCIFormat) entriesPerLayer() int {
	if f.EntriesPerLayer > 0 {
		return f.EntriesPerLayer
	}
	return defaultEntriesPerLayer
}

func (f *OCIFormat) NewWriter(dest string) (Writer, error) {
	if err := os.MkdirAll(filepath.Join(dest, "blobs", "sha256"), 0755); err != nil {
		return nil, fmt.Errorf("create blobs dir: %w", err)
	}
	return &ociWriter{
		dir:             dest,
		entriesPerLayer: f.entriesPerLayer(),
	}, nil
}

func (f *OCIFormat) NewReader(src string) (Reader, error) {
	// Read index.json
	indexData, err := os.ReadFile(filepath.Join(src, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("read index.json: %w", err)
	}

	var idx ociIndex
	if err := json.Unmarshal(indexData, &idx); err != nil {
		return nil, fmt.Errorf("parse index.json: %w", err)
	}
	if len(idx.Manifests) == 0 {
		return nil, fmt.Errorf("index.json has no manifests")
	}

	// Read manifest
	manifestData, err := readBlob(src, idx.Manifests[0].Digest)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest ociManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Read config (contains our index mapping)
	configData, err := readBlob(src, manifest.Config.Digest)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg ociConfig
	if err := json.Unmarshal(configData, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Read all layers into memory
	metas := make(map[string][]byte)
	bodies := make(map[string][]byte)

	for _, layerDesc := range manifest.Layers {
		layerData, err := readBlob(src, layerDesc.Digest)
		if err != nil {
			return nil, fmt.Errorf("read layer %s: %w", layerDesc.Digest, err)
		}
		entries := readTarBytes(layerData)
		for name, data := range entries {
			switch {
			case strings.HasSuffix(name, ".meta"):
				key := strings.TrimSuffix(name, ".meta")
				metas[key] = data
			case strings.HasSuffix(name, ".body"):
				key := strings.TrimSuffix(name, ".body")
				bodies[key] = data
			}
		}
	}

	return &ociReader{
		metas:  metas,
		bodies: bodies,
		keys:   cfg.Keys,
	}, nil
}

// ---- OCI JSON structures ----

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Manifests     []ociDescriptor `json:"manifests"`
}

type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

type ociConfig struct {
	Keys []string `json:"keys"`
}

type ociLayout struct {
	ImageLayoutVersion string `json:"imageLayoutVersion"`
}

// ---- pendingEntry ----

type pendingEntry struct {
	key  string
	meta []byte
	body []byte
}

// ---- ociWriter ----

type ociWriter struct {
	dir             string
	entriesPerLayer int
	pending         []pendingEntry
	layers          []ociDescriptor
	keys            []string
}

func (w *ociWriter) Add(ctx context.Context, key string, meta []byte, body io.Reader) error {
	bodyData, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read body for %s: %w", key, err)
	}
	w.pending = append(w.pending, pendingEntry{key: key, meta: meta, body: bodyData})
	w.keys = append(w.keys, key)

	if len(w.pending) >= w.entriesPerLayer {
		if err := w.flushLayer(); err != nil {
			return fmt.Errorf("flush layer: %w", err)
		}
	}
	return nil
}

func (w *ociWriter) flushLayer() error {
	if len(w.pending) == 0 {
		return nil
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, e := range w.pending {
		if err := writeTarEntry(tw, e.key+".meta", e.meta); err != nil {
			return fmt.Errorf("write meta for %s: %w", e.key, err)
		}
		if err := writeTarEntry(tw, e.key+".body", e.body); err != nil {
			return fmt.Errorf("write body for %s: %w", e.key, err)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}

	data := buf.Bytes()
	digest, err := writeBlob(w.dir, data)
	if err != nil {
		return fmt.Errorf("write layer blob: %w", err)
	}

	w.layers = append(w.layers, ociDescriptor{
		MediaType: mediaTypeLayer,
		Digest:    digest,
		Size:      int64(len(data)),
	})

	w.pending = w.pending[:0]
	return nil
}

func (w *ociWriter) Close() error {
	// Flush remaining entries
	if err := w.flushLayer(); err != nil {
		return fmt.Errorf("final flush: %w", err)
	}

	// Write config blob containing the key index
	cfg := ociConfig{Keys: w.keys}
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	cfgDigest, err := writeBlob(w.dir, cfgData)
	if err != nil {
		return fmt.Errorf("write config blob: %w", err)
	}

	// Write manifest
	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     mediaTypeManifest,
		Config: ociDescriptor{
			MediaType: mediaTypeConfig,
			Digest:    cfgDigest,
			Size:      int64(len(cfgData)),
		},
		Layers: w.layers,
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	manifestDigest, err := writeBlob(w.dir, manifestData)
	if err != nil {
		return fmt.Errorf("write manifest blob: %w", err)
	}

	// Write oci-layout file
	layoutData, err := json.Marshal(ociLayout{ImageLayoutVersion: ociLayoutVersion})
	if err != nil {
		return fmt.Errorf("marshal oci-layout: %w", err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, "oci-layout"), layoutData, 0644); err != nil {
		return fmt.Errorf("write oci-layout: %w", err)
	}

	// Write index.json
	idx := ociIndex{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCILayout,
		Manifests: []ociDescriptor{
			{
				MediaType: mediaTypeManifest,
				Digest:    manifestDigest,
				Size:      int64(len(manifestData)),
			},
		},
	}
	idxData, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("marshal index.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, "index.json"), idxData, 0644); err != nil {
		return fmt.Errorf("write index.json: %w", err)
	}

	return nil
}

// ---- ociReader ----

type ociReader struct {
	metas  map[string][]byte
	bodies map[string][]byte
	keys   []string
}

func (r *ociReader) Get(ctx context.Context, key string) ([]byte, io.ReadCloser, error) {
	meta, ok := r.metas[key]
	if !ok {
		return nil, nil, fmt.Errorf("key not found: %s", key)
	}
	body := r.bodies[key]
	return meta, io.NopCloser(bytes.NewReader(body)), nil
}

func (r *ociReader) List(ctx context.Context) ([]string, error) {
	return r.keys, nil
}

func (r *ociReader) Close() error {
	return nil
}

// ---- helpers ----

// writeBlob writes data to blobs/sha256/{hex} and returns the digest string.
func writeBlob(dir string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hex := fmt.Sprintf("%x", sum)
	digest := "sha256:" + hex
	path := filepath.Join(dir, "blobs", "sha256", hex)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write blob %s: %w", hex, err)
	}
	return digest, nil
}

// readBlob reads a blob by its digest string (e.g. "sha256:abc...").
func readBlob(dir, digest string) ([]byte, error) {
	hex := strings.TrimPrefix(digest, "sha256:")
	path := filepath.Join(dir, "blobs", "sha256", hex)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", digest, err)
	}
	return data, nil
}

// writeTarEntry writes a single file entry into a tar writer.
func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name: name,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// readTarBytes reads a tar archive from raw bytes and returns a map of name -> content.
func readTarBytes(data []byte) map[string][]byte {
	result := make(map[string][]byte)
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			break
		}
		result[hdr.Name] = content
	}
	return result
}
