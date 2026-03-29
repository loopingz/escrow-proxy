# Escrow Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a MITM caching proxy for CI/CD dependency caching with tiered storage, portable archives, and three operating modes (serve/record/offline).

**Architecture:** MITM proxy using `elazarl/goproxy` intercepts HTTPS, caches responses by content digest in tiered storage (local L1 + GCS/S3 L2). Archives bundle cached entries into tar.gz, OCI images, or custom CAS format. Cobra CLI with subcommands for each mode.

**Tech Stack:** Go 1.26.1, elazarl/goproxy, spf13/cobra, cloud.google.com/go/storage, aws-sdk-go-v2, oras-go, singleflight

---

## File Map

```
escrow-proxy/
├── cmd/escrow-proxy/main.go           — CLI entrypoint, cobra root + subcommands
├── internal/
│   ├── config/config.go               — YAML config struct + parsing + CLI merge
│   ├── config/config_test.go          — Config loading tests
│   ├── storage/storage.go             — Storage interface definition
│   ├── storage/local.go               — Local filesystem backend
│   ├── storage/local_test.go          — Local storage tests
│   ├── storage/gcs.go                 — GCS backend
│   ├── storage/gcs_test.go            — GCS tests (with emulator)
│   ├── storage/s3.go                  — S3 backend
│   ├── storage/s3_test.go             — S3 tests (with emulator)
│   ├── storage/tiered.go              — Tiered storage multiplexer
│   ├── storage/tiered_test.go         — Tiered storage tests
│   ├── cache/entry.go                 — CacheEntry meta + body types
│   ├── cache/cache.go                 — Cache layer (key computation, get/put)
│   ├── cache/cache_test.go            — Cache tests
│   ├── archive/archive.go             — Archive interfaces
│   ├── archive/targz.go               — tar.gz format
│   ├── archive/targz_test.go          — tar.gz tests
│   ├── archive/cas.go                 — Custom CAS format
│   ├── archive/cas_test.go            — CAS tests
│   ├── archive/oci.go                 — OCI image format
│   ├── archive/oci_registry.go        — OCI push/pull
│   ├── archive/oci_test.go            — OCI tests
│   ├── archive/detect.go              — Format auto-detection
│   ├── archive/detect_test.go         — Detection tests
│   ├── tls/ca.go                      — CA generation + loading
│   ├── tls/ca_test.go                 — CA tests
│   ├── tls/certs.go                   — Leaf cert generation + LRU cache
│   ├── tls/certs_test.go              — Cert tests
│   ├── proxy/proxy.go                 — MITM proxy engine setup
│   ├── proxy/handler.go               — Request/response interception + caching
│   ├── proxy/cache_key.go             — Cache key computation
│   ├── proxy/cache_key_test.go        — Cache key tests
│   ├── proxy/proxy_test.go            — Proxy integration tests
├── go.mod
└── go.sum
```

---

### Task 1: Project Scaffolding & Go Module

**Files:**
- Create: `go.mod`
- Create: `cmd/escrow-proxy/main.go`
- Create: `internal/config/config.go`

- [ ] **Step 1: Initialize Go module**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go mod init github.com/loopingz/escrow-proxy
```

- [ ] **Step 2: Create minimal main.go with cobra root command**

Create `cmd/escrow-proxy/main.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "escrow-proxy",
		Short: "MITM caching proxy for CI/CD dependency caching",
	}

	rootCmd.PersistentFlags().StringP("config", "", "", "path to config file (YAML)")
	rootCmd.PersistentFlags().StringP("listen", "l", ":8080", "bind address")
	rootCmd.PersistentFlags().String("ca-cert", "", "path to CA certificate")
	rootCmd.PersistentFlags().String("ca-key", "", "path to CA private key")
	rootCmd.PersistentFlags().String("cache-key-headers", "Accept,Accept-Encoding", "headers to include in cache key")
	rootCmd.PersistentFlags().String("log-level", "info", "log level: debug, info, warn, error")
	rootCmd.PersistentFlags().String("storage", "local", "comma-separated storage tier list (e.g., local,gcs)")
	rootCmd.PersistentFlags().String("local-dir", "", "local cache directory (default: ~/.escrow-proxy/cache/)")
	rootCmd.PersistentFlags().String("gcs-bucket", "", "GCS bucket name")
	rootCmd.PersistentFlags().String("gcs-prefix", "", "GCS key prefix")
	rootCmd.PersistentFlags().String("s3-bucket", "", "S3 bucket name")
	rootCmd.PersistentFlags().String("s3-prefix", "", "S3 key prefix")
	rootCmd.PersistentFlags().String("s3-region", "", "S3 region")
	rootCmd.PersistentFlags().Duration("upstream-timeout", 30_000_000_000, "upstream request timeout")

	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newRecordCmd())
	rootCmd.AddCommand(newOfflineCmd())
	rootCmd.AddCommand(newCACmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the caching proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("serve mode not yet implemented")
			return nil
		},
	}
}

func newRecordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Start the caching proxy in record mode",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("record mode not yet implemented")
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "", "archive destination (path or registry ref)")
	cmd.Flags().String("format", "", "archive format: tgz, oci, cas (auto-detect if empty)")
	cmd.Flags().Int("oci-entries-per-layer", 1000, "entries per OCI layer")
	return cmd
}

func newOfflineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "offline",
		Short: "Serve only from an archive",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("offline mode not yet implemented")
			return nil
		},
	}
	cmd.Flags().StringP("archive", "a", "", "archive source (path or registry ref)")
	cmd.Flags().Bool("allow-fallback", false, "on cache miss, forward upstream instead of 502")
	return cmd
}

func newCACmd() *cobra.Command {
	caCmd := &cobra.Command{
		Use:   "ca",
		Short: "CA certificate management",
	}
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Print CA certificate PEM to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("ca export not yet implemented")
			return nil
		},
	}
	caCmd.AddCommand(exportCmd)
	return caCmd
}
```

- [ ] **Step 3: Create config struct**

Create `internal/config/config.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen          string        `yaml:"listen"`
	CA              CAConfig      `yaml:"ca"`
	Cache           CacheConfig   `yaml:"cache"`
	Storage         StorageConfig `yaml:"storage"`
	Record          RecordConfig  `yaml:"record"`
	Offline         OfflineConfig `yaml:"offline"`
	LogLevel        string        `yaml:"log_level"`
	UpstreamTimeout time.Duration `yaml:"upstream_timeout"`
}

type CAConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type CacheConfig struct {
	KeyHeaders []string `yaml:"key_headers"`
}

type StorageConfig struct {
	Tiers []StorageTierConfig `yaml:"tiers"`
}

type StorageTierConfig struct {
	Type   string `yaml:"type"`
	Dir    string `yaml:"dir,omitempty"`
	Bucket string `yaml:"bucket,omitempty"`
	Prefix string `yaml:"prefix,omitempty"`
	Region string `yaml:"region,omitempty"`
}

type RecordConfig struct {
	Output             string `yaml:"output"`
	Format             string `yaml:"format"`
	OCIEntriesPerLayer int    `yaml:"oci_entries_per_layer"`
}

type OfflineConfig struct {
	Archive       string `yaml:"archive"`
	AllowFallback bool   `yaml:"allow_fallback"`
}

func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		Listen:          ":8080",
		LogLevel:        "info",
		UpstreamTimeout: 30 * time.Second,
		Cache: CacheConfig{
			KeyHeaders: []string{"Accept", "Accept-Encoding"},
		},
		Storage: StorageConfig{
			Tiers: []StorageTierConfig{
				{Type: "local", Dir: filepath.Join(homeDir, ".escrow-proxy", "cache")},
			},
		},
		Record: RecordConfig{
			OCIEntriesPerLayer: 1000,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	return cfg, nil
}
```

- [ ] **Step 4: Install dependencies and verify build**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go get github.com/spf13/cobra
go get gopkg.in/yaml.v3
go build ./cmd/escrow-proxy/
```

- [ ] **Step 5: Verify CLI runs**

```bash
./escrow-proxy --help
./escrow-proxy serve --help
./escrow-proxy record --help
./escrow-proxy offline --help
./escrow-proxy ca export
```

Expected: help output for each command, "not yet implemented" for ca export.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/ internal/config/
git commit -m "feat: scaffold project with cobra CLI and config parsing"
```

---

### Task 2: Storage Interface & Local Backend

**Files:**
- Create: `internal/storage/storage.go`
- Create: `internal/storage/local.go`
- Create: `internal/storage/local_test.go`

- [ ] **Step 1: Write the storage interface**

Create `internal/storage/storage.go`:

```go
package storage

import (
	"context"
	"errors"
	"io"
)

var ErrNotFound = errors.New("key not found")

type Storage interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader) error
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}
```

- [ ] **Step 2: Write failing tests for local storage**

Create `internal/storage/local_test.go`:

```go
package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestLocalStorage_PutAndGet(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	data := []byte("hello world")
	if err := s.Put(ctx, "test-key", bytes.NewReader(data)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, "test-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q, want %q", got, data)
	}
}

func TestLocalStorage_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestLocalStorage_Exists(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	exists, err := s.Exists(ctx, "missing")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("expected false for missing key")
	}

	if err := s.Put(ctx, "present", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err = s.Exists(ctx, "present")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected true for present key")
	}
}

func TestLocalStorage_Delete(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	if err := s.Put(ctx, "to-delete", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, err := s.Exists(ctx, "to-delete")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("expected key to be deleted")
	}
}

func TestLocalStorage_List(t *testing.T) {
	dir := t.TempDir()
	s := storage.NewLocal(dir)
	ctx := context.Background()

	keys := []string{"abc123.meta", "abc123.body", "def456.meta", "def456.body"}
	for _, k := range keys {
		if err := s.Put(ctx, k, bytes.NewReader([]byte("data"))); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	got, err := s.List(ctx, "abc")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 keys with prefix 'abc', got %d: %v", len(got), got)
	}

	all, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 keys total, got %d", len(all))
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go test ./internal/storage/ -v
```

Expected: FAIL — `NewLocal` not defined.

- [ ] **Step 4: Implement local storage**

Create `internal/storage/local.go`:

```go
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
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/storage/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/storage/
git commit -m "feat: add storage interface and local filesystem backend"
```

---

### Task 3: Tiered Storage

**Files:**
- Create: `internal/storage/tiered.go`
- Create: `internal/storage/tiered_test.go`

- [ ] **Step 1: Write failing tests for tiered storage**

Create `internal/storage/tiered_test.go`:

```go
package storage_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestTieredStorage_GetPromotesToL1(t *testing.T) {
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	l1 := storage.NewLocal(l1Dir)
	l2 := storage.NewLocal(l2Dir)
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	// Put only in L2
	if err := l2.Put(ctx, "key1", bytes.NewReader([]byte("from-l2"))); err != nil {
		t.Fatalf("L2 Put: %v", err)
	}

	// Get from tiered should find it and promote to L1
	rc, err := tiered.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Tiered Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "from-l2" {
		t.Fatalf("got %q, want %q", got, "from-l2")
	}

	// L1 should now have it
	exists, err := l1.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("L1 Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected key to be promoted to L1")
	}
}

func TestTieredStorage_PutWritesToAllTiers(t *testing.T) {
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	l1 := storage.NewLocal(l1Dir)
	l2 := storage.NewLocal(l2Dir)
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	if err := tiered.Put(ctx, "key1", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Tiered Put: %v", err)
	}

	for i, s := range []storage.Storage{l1, l2} {
		exists, err := s.Exists(ctx, "key1")
		if err != nil {
			t.Fatalf("tier %d Exists: %v", i, err)
		}
		if !exists {
			t.Fatalf("expected key in tier %d", i)
		}
	}
}

func TestTieredStorage_GetNotFound(t *testing.T) {
	l1 := storage.NewLocal(t.TempDir())
	l2 := storage.NewLocal(t.TempDir())
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	_, err := tiered.Get(ctx, "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTieredStorage_DeleteFromAllTiers(t *testing.T) {
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	l1 := storage.NewLocal(l1Dir)
	l2 := storage.NewLocal(l2Dir)
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	if err := tiered.Put(ctx, "key1", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := tiered.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for i, s := range []storage.Storage{l1, l2} {
		exists, _ := s.Exists(ctx, "key1")
		if exists {
			t.Fatalf("expected key deleted from tier %d", i)
		}
	}
}

func TestTieredStorage_ExistsReturnsOnFirstHit(t *testing.T) {
	l1 := storage.NewLocal(t.TempDir())
	l2 := storage.NewLocal(t.TempDir())
	tiered := storage.NewTiered([]storage.Storage{l1, l2})
	ctx := context.Background()

	// Only in L1
	if err := l1.Put(ctx, "key1", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("L1 Put: %v", err)
	}

	exists, err := tiered.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/storage/ -v -run TestTiered
```

Expected: FAIL — `NewTiered` not defined.

- [ ] **Step 3: Implement tiered storage**

Create `internal/storage/tiered.go`:

```go
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
		// Read fully to promote to earlier tiers
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading from tier %d: %w", i, err)
		}
		// Backfill tiers 0..i-1
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
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/storage/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/tiered.go internal/storage/tiered_test.go
git commit -m "feat: add tiered storage multiplexer with L1 promotion"
```

---

### Task 4: Cache Entry Types & Cache Key Computation

**Files:**
- Create: `internal/cache/entry.go`
- Create: `internal/proxy/cache_key.go`
- Create: `internal/proxy/cache_key_test.go`

- [ ] **Step 1: Create cache entry types**

Create `internal/cache/entry.go`:

```go
package cache

import (
	"encoding/json"
	"net/http"
)

type EntryMeta struct {
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
}

func MarshalMeta(meta *EntryMeta) ([]byte, error) {
	return json.Marshal(meta)
}

func UnmarshalMeta(data []byte) (*EntryMeta, error) {
	var meta EntryMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}
```

- [ ] **Step 2: Write failing tests for cache key**

Create `internal/proxy/cache_key_test.go`:

```go
package proxy_test

import (
	"net/http"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/proxy"
)

func TestComputeCacheKey_Deterministic(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req1.Header.Set("Accept", "application/json")

	req2, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2.Header.Set("Accept", "application/json")

	headers := []string{"Accept"}
	k1 := proxy.ComputeCacheKey(req1, headers)
	k2 := proxy.ComputeCacheKey(req2, headers)

	if k1 != k2 {
		t.Fatalf("same request produced different keys: %s vs %s", k1, k2)
	}
}

func TestComputeCacheKey_DifferentURLsDiffer(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2, _ := http.NewRequest("GET", "https://example.com/bar", nil)

	k1 := proxy.ComputeCacheKey(req1, nil)
	k2 := proxy.ComputeCacheKey(req2, nil)

	if k1 == k2 {
		t.Fatal("different URLs should produce different keys")
	}
}

func TestComputeCacheKey_DifferentMethodsDiffer(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2, _ := http.NewRequest("POST", "https://example.com/foo", nil)

	k1 := proxy.ComputeCacheKey(req1, nil)
	k2 := proxy.ComputeCacheKey(req2, nil)

	if k1 == k2 {
		t.Fatal("different methods should produce different keys")
	}
}

func TestComputeCacheKey_HeaderOrderIrrelevant(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req1.Header.Set("Accept", "text/html")
	req1.Header.Set("Accept-Encoding", "gzip")

	req2, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	req2.Header.Set("Accept", "text/html")

	headers := []string{"Accept-Encoding", "Accept"}
	k1 := proxy.ComputeCacheKey(req1, headers)
	k2 := proxy.ComputeCacheKey(req2, headers)

	if k1 != k2 {
		t.Fatalf("header order should not matter: %s vs %s", k1, k2)
	}
}

func TestComputeCacheKey_IgnoresNonSelectedHeaders(t *testing.T) {
	req1, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req1.Header.Set("Accept", "text/html")
	req1.Header.Set("User-Agent", "curl")

	req2, _ := http.NewRequest("GET", "https://example.com/foo", nil)
	req2.Header.Set("Accept", "text/html")
	req2.Header.Set("User-Agent", "wget")

	headers := []string{"Accept"}
	k1 := proxy.ComputeCacheKey(req1, headers)
	k2 := proxy.ComputeCacheKey(req2, headers)

	if k1 != k2 {
		t.Fatal("non-selected headers should not affect key")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/proxy/ -v
```

Expected: FAIL — `ComputeCacheKey` not defined.

- [ ] **Step 4: Implement cache key computation**

Create `internal/proxy/cache_key.go`:

```go
package proxy

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func ComputeCacheKey(req *http.Request, selectedHeaders []string) string {
	h := sha256.New()
	h.Write([]byte(req.Method))
	h.Write([]byte("\n"))
	h.Write([]byte(req.URL.String()))
	h.Write([]byte("\n"))

	// Sort selected headers for determinism
	sorted := make([]string, len(selectedHeaders))
	copy(sorted, selectedHeaders)
	sort.Strings(sorted)

	var headerParts []string
	for _, name := range sorted {
		vals := req.Header.Values(name)
		sort.Strings(vals)
		for _, v := range vals {
			headerParts = append(headerParts, fmt.Sprintf("%s:%s", strings.ToLower(name), v))
		}
	}
	h.Write([]byte(strings.Join(headerParts, "\n")))
	h.Write([]byte("\n"))

	// Body hash
	if req.Body != nil && req.Body != http.NoBody {
		bodyHash := sha256.New()
		io.Copy(bodyHash, req.Body)
		h.Write(bodyHash.Sum(nil))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/proxy/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cache/ internal/proxy/
git commit -m "feat: add cache entry types and cache key computation"
```

---

### Task 5: TLS Certificate Management

**Files:**
- Create: `internal/tls/ca.go`
- Create: `internal/tls/ca_test.go`
- Create: `internal/tls/certs.go`
- Create: `internal/tls/certs_test.go`

- [ ] **Step 1: Write failing tests for CA generation**

Create `internal/tls/ca_test.go`:

```go
package tls_test

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

func TestGenerateCA(t *testing.T) {
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if ca.Cert == nil {
		t.Fatal("expected non-nil cert")
	}
	if ca.Key == nil {
		t.Fatal("expected non-nil key")
	}
	if !ca.Cert.IsCA {
		t.Fatal("expected CA certificate")
	}
}

func TestSaveAndLoadCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	if err := tlspkg.SaveCA(ca, certPath, keyPath); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}

	loaded, err := tlspkg.LoadCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}

	if !loaded.Cert.Equal(ca.Cert) {
		t.Fatal("loaded cert doesn't match original")
	}
}

func TestEnsureCA_GeneratesOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	ca, err := tlspkg.EnsureCA(dir, "", "")
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if ca.Cert == nil {
		t.Fatal("expected non-nil cert")
	}

	// Files should exist
	if _, err := os.Stat(filepath.Join(dir, "ca.crt")); err != nil {
		t.Fatalf("ca.crt not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatalf("ca.key not found: %v", err)
	}
}

func TestEnsureCA_ReusesExisting(t *testing.T) {
	dir := t.TempDir()
	ca1, _ := tlspkg.EnsureCA(dir, "", "")
	ca2, _ := tlspkg.EnsureCA(dir, "", "")

	if !ca1.Cert.Equal(ca2.Cert) {
		t.Fatal("expected same CA on second call")
	}
}

func TestExportCAPEM(t *testing.T) {
	ca, _ := tlspkg.GenerateCA()
	pem := tlspkg.ExportCAPEM(ca)
	if len(pem) == 0 {
		t.Fatal("expected non-empty PEM")
	}

	// Should be parseable
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("PEM not parseable as certificate")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/tls/ -v
```

Expected: FAIL — package not found.

- [ ] **Step 3: Implement CA management**

Create `internal/tls/ca.go`:

```go
package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	Raw  []byte // DER-encoded certificate
}

func GenerateCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Escrow Proxy"},
			CommonName:   "Escrow Proxy CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate: %w", err)
	}

	return &CA{Cert: cert, Key: key, Raw: raw}, nil
}

func SaveCA(ca *CA, certPath, keyPath string) error {
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("writing cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(ca.Key)
	if err != nil {
		return fmt.Errorf("marshaling key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("writing key: %w", err)
	}

	return nil
}

func LoadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing cert: %w", err)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("no PEM block found in %s", keyPath)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing key: %w", err)
	}

	return &CA{Cert: cert, Key: key, Raw: block.Bytes}, nil
}

func EnsureCA(dir, certPath, keyPath string) (*CA, error) {
	if certPath != "" && keyPath != "" {
		return LoadCA(certPath, keyPath)
	}

	defaultCert := filepath.Join(dir, "ca.crt")
	defaultKey := filepath.Join(dir, "ca.key")

	if _, err := os.Stat(defaultCert); err == nil {
		return LoadCA(defaultCert, defaultKey)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating CA directory: %w", err)
	}

	ca, err := GenerateCA()
	if err != nil {
		return nil, err
	}

	if err := SaveCA(ca, defaultCert, defaultKey); err != nil {
		return nil, err
	}

	return ca, nil
}

func ExportCAPEM(ca *CA) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
}
```

- [ ] **Step 4: Run CA tests to verify they pass**

```bash
go test ./internal/tls/ -v -run TestGenerateCA -run TestSaveAndLoadCA -run TestEnsureCA -run TestExportCAPEM
```

Expected: all PASS.

- [ ] **Step 5: Write failing tests for leaf cert generation**

Create `internal/tls/certs_test.go`:

```go
package tls_test

import (
	"testing"

	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

func TestCertCache_GeneratesLeafCert(t *testing.T) {
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	cache := tlspkg.NewCertCache(ca, 100)
	tlsCert, err := cache.GetOrCreate("example.com")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if tlsCert.Leaf == nil {
		t.Fatal("expected non-nil leaf")
	}
	if tlsCert.Leaf.Subject.CommonName != "example.com" {
		t.Fatalf("expected CN=example.com, got %s", tlsCert.Leaf.Subject.CommonName)
	}
}

func TestCertCache_CachesResult(t *testing.T) {
	ca, _ := tlspkg.GenerateCA()
	cache := tlspkg.NewCertCache(ca, 100)

	cert1, _ := cache.GetOrCreate("example.com")
	cert2, _ := cache.GetOrCreate("example.com")

	if cert1.Leaf.SerialNumber.Cmp(cert2.Leaf.SerialNumber) != 0 {
		t.Fatal("expected same cert on second call (cached)")
	}
}

func TestCertCache_DifferentHostsDifferentCerts(t *testing.T) {
	ca, _ := tlspkg.GenerateCA()
	cache := tlspkg.NewCertCache(ca, 100)

	cert1, _ := cache.GetOrCreate("example.com")
	cert2, _ := cache.GetOrCreate("other.com")

	if cert1.Leaf.SerialNumber.Cmp(cert2.Leaf.SerialNumber) == 0 {
		t.Fatal("expected different certs for different hosts")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

```bash
go test ./internal/tls/ -v -run TestCertCache
```

Expected: FAIL — `NewCertCache` not defined.

- [ ] **Step 7: Implement leaf cert generation with LRU cache**

Create `internal/tls/certs.go`:

```go
package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"sync"
	"time"
)

type CertCache struct {
	ca       *CA
	mu       sync.Mutex
	cache    map[string]*cacheEntry
	order    []string
	maxSize  int
}

type cacheEntry struct {
	cert *tls.Certificate
}

func NewCertCache(ca *CA, maxSize int) *CertCache {
	return &CertCache{
		ca:      ca,
		cache:   make(map[string]*cacheEntry),
		maxSize: maxSize,
	}
}

func (c *CertCache) GetOrCreate(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.cache[host]; ok {
		return entry.cert, nil
	}

	cert, err := c.generate(host)
	if err != nil {
		return nil, err
	}

	c.cache[host] = &cacheEntry{cert: cert}
	c.order = append(c.order, host)

	if len(c.order) > c.maxSize {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.cache, evict)
	}

	return cert, nil
}

func (c *CertCache) generate(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}

	raw, err := x509.CreateCertificate(rand.Reader, template, c.ca.Cert, &key.PublicKey, c.ca.Key)
	if err != nil {
		return nil, fmt.Errorf("creating leaf cert: %w", err)
	}

	leaf, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing leaf cert: %w", err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{raw, c.ca.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}
```

- [ ] **Step 8: Run all TLS tests**

```bash
go test ./internal/tls/ -v
```

Expected: all PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/tls/
git commit -m "feat: add TLS CA generation, leaf cert cache, and PEM export"
```

---

### Task 6: Cache Layer

**Files:**
- Create: `internal/cache/cache.go`
- Create: `internal/cache/cache_test.go`

- [ ] **Step 1: Write failing tests for cache**

Create `internal/cache/cache_test.go`:

```go
package cache_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestCache_PutAndGet(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	meta := &cache.EntryMeta{
		Method:     "GET",
		URL:        "https://example.com/pkg",
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/octet-stream"}},
	}
	body := []byte("package-contents")

	key := "abc123"
	if err := c.Put(ctx, key, meta, bytes.NewReader(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	gotMeta, gotBody, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer gotBody.Close()

	if gotMeta.URL != meta.URL {
		t.Fatalf("URL: got %s, want %s", gotMeta.URL, meta.URL)
	}
	if gotMeta.StatusCode != 200 {
		t.Fatalf("StatusCode: got %d, want 200", gotMeta.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(gotBody)
	if !bytes.Equal(bodyBytes, body) {
		t.Fatalf("body: got %q, want %q", bodyBytes, body)
	}
}

func TestCache_GetMiss(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	_, _, err := c.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error on cache miss")
	}
}

func TestCache_Exists(t *testing.T) {
	s := storage.NewLocal(t.TempDir())
	c := cache.New(s)
	ctx := context.Background()

	exists, _ := c.Exists(ctx, "missing")
	if exists {
		t.Fatal("expected false")
	}

	meta := &cache.EntryMeta{Method: "GET", URL: "https://example.com", StatusCode: 200}
	c.Put(ctx, "present", meta, bytes.NewReader([]byte("data")))

	exists, _ = c.Exists(ctx, "present")
	if !exists {
		t.Fatal("expected true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/cache/ -v
```

Expected: FAIL — `cache.New` not defined.

- [ ] **Step 3: Implement cache layer**

Create `internal/cache/cache.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/cache/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/cache.go internal/cache/cache_test.go
git commit -m "feat: add cache layer with meta/body storage"
```

---

### Task 7: Archive Interfaces & tar.gz Format

**Files:**
- Create: `internal/archive/archive.go`
- Create: `internal/archive/targz.go`
- Create: `internal/archive/targz_test.go`

- [ ] **Step 1: Create archive interfaces**

Create `internal/archive/archive.go`:

```go
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
```

- [ ] **Step 2: Write failing tests for tar.gz**

Create `internal/archive/targz_test.go`:

```go
package archive_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestTarGz_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.tar.gz")
	ctx := context.Background()

	format := &archive.TarGzFormat{}

	// Write
	w, err := format.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	entries := map[string]struct{ meta, body string }{
		"key1": {meta: `{"url":"https://example.com/a"}`, body: "body-a"},
		"key2": {meta: `{"url":"https://example.com/b"}`, body: "body-b"},
	}

	for k, v := range entries {
		if err := w.Add(ctx, k, []byte(v.meta), bytes.NewReader([]byte(v.body))); err != nil {
			t.Fatalf("Add %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("archive file not found: %v", err)
	}

	// Read
	r, err := format.NewReader(path)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	keys, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "key1" || keys[1] != "key2" {
		t.Fatalf("List: got %v, want [key1 key2]", keys)
	}

	for k, want := range entries {
		meta, bodyRC, err := r.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		body, _ := io.ReadAll(bodyRC)
		bodyRC.Close()

		if string(meta) != want.meta {
			t.Fatalf("Get %s meta: got %q, want %q", k, meta, want.meta)
		}
		if string(body) != want.body {
			t.Fatalf("Get %s body: got %q, want %q", k, body, want.body)
		}
	}
}

func TestTarGz_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.tar.gz")
	ctx := context.Background()

	format := &archive.TarGzFormat{}
	w, _ := format.NewWriter(path)
	w.Add(ctx, "key1", []byte("meta"), bytes.NewReader([]byte("body")))
	w.Close()

	r, _ := format.NewReader(path)
	defer r.Close()

	_, _, err := r.Get(ctx, "missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/archive/ -v
```

Expected: FAIL — `TarGzFormat` not defined.

- [ ] **Step 4: Implement tar.gz format**

Create `internal/archive/targz.go`:

```go
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
)

type TarGzFormat struct{}

func (f *TarGzFormat) NewWriter(dest string) (Writer, error) {
	file, err := os.Create(dest)
	if err != nil {
		return nil, fmt.Errorf("creating archive file: %w", err)
	}
	gw := gzip.NewWriter(file)
	tw := tar.NewWriter(gw)
	return &tarGzWriter{
		file:    file,
		gw:      gw,
		tw:      tw,
		entries: make(map[string]struct{}),
	}, nil
}

func (f *TarGzFormat) NewReader(src string) (Reader, error) {
	// Read entire archive into memory for random access
	index, metas, bodies, err := readTarGz(src)
	if err != nil {
		return nil, err
	}
	return &tarGzReader{index: index, metas: metas, bodies: bodies}, nil
}

type tarGzWriter struct {
	file    *os.File
	gw      *gzip.Writer
	tw      *tar.Writer
	entries map[string]struct{}
}

func (w *tarGzWriter) Add(_ context.Context, key string, meta []byte, body io.Reader) error {
	w.entries[key] = struct{}{}

	// Write meta
	if err := w.writeFile(key+".meta", meta); err != nil {
		return fmt.Errorf("writing meta for %s: %w", key, err)
	}

	// Write body
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("reading body for %s: %w", key, err)
	}
	if err := w.writeFile(key+".body", bodyBytes); err != nil {
		return fmt.Errorf("writing body for %s: %w", key, err)
	}

	return nil
}

func (w *tarGzWriter) writeFile(name string, data []byte) error {
	hdr := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := w.tw.Write(data)
	return err
}

func (w *tarGzWriter) Close() error {
	// Write index
	keys := make([]string, 0, len(w.entries))
	for k := range w.entries {
		keys = append(keys, k)
	}
	indexData, err := json.Marshal(keys)
	if err != nil {
		return fmt.Errorf("marshaling index: %w", err)
	}
	if err := w.writeFile("index.json", indexData); err != nil {
		return fmt.Errorf("writing index: %w", err)
	}

	if err := w.tw.Close(); err != nil {
		return fmt.Errorf("closing tar: %w", err)
	}
	if err := w.gw.Close(); err != nil {
		return fmt.Errorf("closing gzip: %w", err)
	}
	return w.file.Close()
}

type tarGzReader struct {
	index  []string
	metas  map[string][]byte
	bodies map[string][]byte
}

func readTarGz(path string) ([]string, map[string][]byte, map[string][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening archive: %w", err)
	}
	defer file.Close()

	gr, err := gzip.NewReader(file)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating gzip reader: %w", err)
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
			return nil, nil, nil, fmt.Errorf("reading tar: %w", err)
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}

		if hdr.Name == "index.json" {
			if err := json.Unmarshal(data, &index); err != nil {
				return nil, nil, nil, fmt.Errorf("parsing index: %w", err)
			}
			continue
		}

		name := hdr.Name
		if len(name) > 5 && name[len(name)-5:] == ".meta" {
			key := name[:len(name)-5]
			metas[key] = data
		} else if len(name) > 5 && name[len(name)-5:] == ".body" {
			key := name[:len(name)-5]
			bodies[key] = data
		}
	}

	return index, metas, bodies, nil
}

func (r *tarGzReader) Get(_ context.Context, key string) ([]byte, io.ReadCloser, error) {
	meta, ok := r.metas[key]
	if !ok {
		return nil, nil, fmt.Errorf("key not found in archive: %s", key)
	}
	body, ok := r.bodies[key]
	if !ok {
		return nil, nil, fmt.Errorf("body not found in archive: %s", key)
	}
	return meta, io.NopCloser(bytes.NewReader(body)), nil
}

func (r *tarGzReader) List(_ context.Context) ([]string, error) {
	return r.index, nil
}

func (r *tarGzReader) Close() error {
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/archive/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/archive/archive.go internal/archive/targz.go internal/archive/targz_test.go
git commit -m "feat: add archive interfaces and tar.gz format implementation"
```

---

### Task 8: Custom CAS Archive Format

**Files:**
- Create: `internal/archive/cas.go`
- Create: `internal/archive/cas_test.go`

- [ ] **Step 1: Write failing tests for CAS**

Create `internal/archive/cas_test.go`:

```go
package archive_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestCAS_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cas")
	ctx := context.Background()

	format := &archive.CASFormat{}

	w, err := format.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	entries := map[string]struct{ meta, body string }{
		"key1": {meta: `{"url":"https://example.com/a"}`, body: "body-a"},
		"key2": {meta: `{"url":"https://example.com/b"}`, body: "body-b"},
	}

	for k, v := range entries {
		if err := w.Add(ctx, k, []byte(v.meta), bytes.NewReader([]byte(v.body))); err != nil {
			t.Fatalf("Add %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	r, err := format.NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	keys, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "key1" || keys[1] != "key2" {
		t.Fatalf("List: got %v", keys)
	}

	for k, want := range entries {
		meta, bodyRC, err := r.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		body, _ := io.ReadAll(bodyRC)
		bodyRC.Close()

		if string(meta) != want.meta {
			t.Fatalf("Get %s meta: got %q, want %q", k, meta, want.meta)
		}
		if string(body) != want.body {
			t.Fatalf("Get %s body: got %q, want %q", k, body, want.body)
		}
	}
}

func TestCAS_DedupesIdenticalBodies(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cas")
	ctx := context.Background()

	format := &archive.CASFormat{}
	w, _ := format.NewWriter(dir)

	sameBody := "identical-content"
	w.Add(ctx, "key1", []byte(`{"url":"a"}`), bytes.NewReader([]byte(sameBody)))
	w.Add(ctx, "key2", []byte(`{"url":"b"}`), bytes.NewReader([]byte(sameBody)))
	w.Close()

	r, _ := format.NewReader(dir)
	defer r.Close()

	_, b1, _ := r.Get(ctx, "key1")
	body1, _ := io.ReadAll(b1)
	b1.Close()
	_, b2, _ := r.Get(ctx, "key2")
	body2, _ := io.ReadAll(b2)
	b2.Close()

	if string(body1) != sameBody || string(body2) != sameBody {
		t.Fatal("expected identical bodies")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/archive/ -v -run TestCAS
```

Expected: FAIL — `CASFormat` not defined.

- [ ] **Step 3: Implement CAS format**

Create `internal/archive/cas.go`:

```go
package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type CASFormat struct{}

type casIndexEntry struct {
	MetaDigest string `json:"meta_digest"`
	BodyDigest string `json:"body_digest"`
}

func (f *CASFormat) NewWriter(dest string) (Writer, error) {
	blobDir := filepath.Join(dest, "blobs", "sha256")
	metaDir := filepath.Join(dest, "meta")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating blob dir: %w", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating meta dir: %w", err)
	}
	return &casWriter{
		dir:   dest,
		index: make(map[string]casIndexEntry),
	}, nil
}

func (f *CASFormat) NewReader(src string) (Reader, error) {
	indexPath := filepath.Join(src, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("reading index: %w", err)
	}
	var index map[string]casIndexEntry
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("parsing index: %w", err)
	}
	return &casReader{dir: src, index: index}, nil
}

type casWriter struct {
	dir   string
	index map[string]casIndexEntry
}

func (w *casWriter) Add(_ context.Context, key string, meta []byte, body io.Reader) error {
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}

	bodyDigest := fmt.Sprintf("%x", sha256.Sum256(bodyBytes))
	metaDigest := fmt.Sprintf("%x", sha256.Sum256(meta))

	// Write body blob (deduped by digest)
	blobPath := filepath.Join(w.dir, "blobs", "sha256", bodyDigest)
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		if err := os.WriteFile(blobPath, bodyBytes, 0o644); err != nil {
			return fmt.Errorf("writing body blob: %w", err)
		}
	}

	// Write meta
	metaPath := filepath.Join(w.dir, "meta", key+".json")
	if err := os.WriteFile(metaPath, meta, 0o644); err != nil {
		return fmt.Errorf("writing meta: %w", err)
	}

	w.index[key] = casIndexEntry{
		MetaDigest: metaDigest,
		BodyDigest: bodyDigest,
	}
	return nil
}

func (w *casWriter) Close() error {
	indexData, err := json.MarshalIndent(w.index, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling index: %w", err)
	}
	indexPath := filepath.Join(w.dir, "index.json")
	return os.WriteFile(indexPath, indexData, 0o644)
}

type casReader struct {
	dir   string
	index map[string]casIndexEntry
}

func (r *casReader) Get(_ context.Context, key string) ([]byte, io.ReadCloser, error) {
	entry, ok := r.index[key]
	if !ok {
		return nil, nil, fmt.Errorf("key not found in CAS: %s", key)
	}

	meta, err := os.ReadFile(filepath.Join(r.dir, "meta", key+".json"))
	if err != nil {
		return nil, nil, fmt.Errorf("reading meta: %w", err)
	}

	bodyPath := filepath.Join(r.dir, "blobs", "sha256", entry.BodyDigest)
	bodyFile, err := os.Open(bodyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening body: %w", err)
	}

	return meta, bodyFile, nil
}

func (r *casReader) List(_ context.Context) ([]string, error) {
	keys := make([]string, 0, len(r.index))
	for k := range r.index {
		keys = append(keys, k)
	}
	return keys, nil
}

func (r *casReader) Close() error {
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/archive/ -v -run TestCAS
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/cas.go internal/archive/cas_test.go
git commit -m "feat: add custom CAS archive format with content deduplication"
```

---

### Task 9: OCI Archive Format

**Files:**
- Create: `internal/archive/oci.go`
- Create: `internal/archive/oci_registry.go`
- Create: `internal/archive/oci_test.go`

- [ ] **Step 1: Write failing tests for OCI format**

Create `internal/archive/oci_test.go`:

```go
package archive_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestOCI_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci")
	ctx := context.Background()

	format := &archive.OCIFormat{EntriesPerLayer: 2}

	w, err := format.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	entries := map[string]struct{ meta, body string }{
		"key1": {meta: `{"url":"https://example.com/a"}`, body: "body-a"},
		"key2": {meta: `{"url":"https://example.com/b"}`, body: "body-b"},
		"key3": {meta: `{"url":"https://example.com/c"}`, body: "body-c"},
	}

	for k, v := range entries {
		if err := w.Add(ctx, k, []byte(v.meta), bytes.NewReader([]byte(v.body))); err != nil {
			t.Fatalf("Add %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	r, err := format.NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	keys, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(keys)
	if len(keys) != 3 {
		t.Fatalf("List: got %d keys, want 3", len(keys))
	}

	for k, want := range entries {
		meta, bodyRC, err := r.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		body, _ := io.ReadAll(bodyRC)
		bodyRC.Close()

		if string(meta) != want.meta {
			t.Fatalf("Get %s meta: got %q, want %q", k, meta, want.meta)
		}
		if string(body) != want.body {
			t.Fatalf("Get %s body: got %q, want %q", k, body, want.body)
		}
	}
}

func TestOCI_LayerGrouping(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci")
	ctx := context.Background()

	// 5 entries with 2 per layer = 3 layers
	format := &archive.OCIFormat{EntriesPerLayer: 2}
	w, _ := format.NewWriter(dir)

	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key%d", i)
		w.Add(ctx, key, []byte(`{"url":"test"}`), bytes.NewReader([]byte("body")))
	}
	w.Close()

	r, _ := format.NewReader(dir)
	defer r.Close()

	keys, _ := r.List(ctx)
	if len(keys) != 5 {
		t.Fatalf("expected 5 keys, got %d", len(keys))
	}
}
```

Add the missing import at the top of the test file (update after step 2 if needed):

```go
import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/archive/ -v -run TestOCI
```

Expected: FAIL — `OCIFormat` not defined.

- [ ] **Step 3: Implement OCI format (local layout)**

Create `internal/archive/oci.go`:

```go
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
)

type OCIFormat struct {
	EntriesPerLayer int
}

type ociConfig struct {
	Index map[string]ociEntryRef `json:"index"`
}

type ociEntryRef struct {
	LayerDigest string `json:"layer_digest"`
	MetaName    string `json:"meta_name"`
	BodyName    string `json:"body_name"`
}

type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func (f *OCIFormat) NewWriter(dest string) (Writer, error) {
	blobDir := filepath.Join(dest, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating blobs dir: %w", err)
	}

	entriesPerLayer := f.EntriesPerLayer
	if entriesPerLayer <= 0 {
		entriesPerLayer = 1000
	}

	return &ociWriter{
		dir:             dest,
		entriesPerLayer: entriesPerLayer,
		index:           make(map[string]ociEntryRef),
	}, nil
}

func (f *OCIFormat) NewReader(src string) (Reader, error) {
	// Read the OCI index.json to find the manifest
	indexPath := filepath.Join(src, "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("reading OCI index: %w", err)
	}

	var ociIndex struct {
		Manifests []ociDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(indexData, &ociIndex); err != nil {
		return nil, fmt.Errorf("parsing OCI index: %w", err)
	}
	if len(ociIndex.Manifests) == 0 {
		return nil, fmt.Errorf("no manifests found in OCI index")
	}

	// Read manifest
	manifestDigest := ociIndex.Manifests[0].Digest
	manifestData, err := readBlob(src, manifestDigest)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	// Read config (contains our index)
	configData, err := readBlob(src, manifest.Config.Digest)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var config ociConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Read all layers into memory
	layers := make(map[string]map[string][]byte) // digest -> filename -> data
	for _, layer := range manifest.Layers {
		layerData, err := readBlob(src, layer.Digest)
		if err != nil {
			return nil, fmt.Errorf("reading layer %s: %w", layer.Digest, err)
		}
		files, err := readTarBytes(layerData)
		if err != nil {
			return nil, fmt.Errorf("reading layer tar %s: %w", layer.Digest, err)
		}
		layers[layer.Digest] = files
	}

	return &ociReader{config: config, layers: layers}, nil
}

type ociWriter struct {
	dir             string
	entriesPerLayer int
	index           map[string]ociEntryRef
	currentEntries  []pendingEntry
	layers          []ociDescriptor
}

type pendingEntry struct {
	key  string
	meta []byte
	body []byte
}

func (w *ociWriter) Add(_ context.Context, key string, meta []byte, body io.Reader) error {
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}

	w.currentEntries = append(w.currentEntries, pendingEntry{key: key, meta: meta, body: bodyBytes})

	if len(w.currentEntries) >= w.entriesPerLayer {
		return w.flushLayer()
	}

	return nil
}

func (w *ociWriter) flushLayer() error {
	if len(w.currentEntries) == 0 {
		return nil
	}

	// Build a tar of the entries
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, e := range w.currentEntries {
		metaName := e.key + ".meta"
		bodyName := e.key + ".body"

		if err := writeTarEntry(tw, metaName, e.meta); err != nil {
			return err
		}
		if err := writeTarEntry(tw, bodyName, e.body); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar: %w", err)
	}

	layerData := buf.Bytes()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(layerData))

	if err := writeBlob(w.dir, digest, layerData); err != nil {
		return fmt.Errorf("writing layer blob: %w", err)
	}

	for _, e := range w.currentEntries {
		w.index[e.key] = ociEntryRef{
			LayerDigest: digest,
			MetaName:    e.key + ".meta",
			BodyName:    e.key + ".body",
		}
	}

	w.layers = append(w.layers, ociDescriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar",
		Digest:    digest,
		Size:      int64(len(layerData)),
	})

	w.currentEntries = w.currentEntries[:0]
	return nil
}

func (w *ociWriter) Close() error {
	// Flush remaining entries
	if err := w.flushLayer(); err != nil {
		return err
	}

	// Write config blob
	config := ociConfig{Index: w.index}
	configData, _ := json.Marshal(config)
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(configData))
	if err := writeBlob(w.dir, configDigest, configData); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	// Write manifest
	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: ociDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    configDigest,
			Size:      int64(len(configData)),
		},
		Layers: w.layers,
	}
	manifestData, _ := json.Marshal(manifest)
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))
	if err := writeBlob(w.dir, manifestDigest, manifestData); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	// Write oci-layout
	ociLayout := `{"imageLayoutVersion":"1.0.0"}`
	if err := os.WriteFile(filepath.Join(w.dir, "oci-layout"), []byte(ociLayout), 0o644); err != nil {
		return fmt.Errorf("writing oci-layout: %w", err)
	}

	// Write index.json
	ociIndex := struct {
		SchemaVersion int             `json:"schemaVersion"`
		Manifests     []ociDescriptor `json:"manifests"`
	}{
		SchemaVersion: 2,
		Manifests: []ociDescriptor{
			{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    manifestDigest,
				Size:      int64(len(manifestData)),
			},
		},
	}
	indexData, _ := json.Marshal(ociIndex)
	return os.WriteFile(filepath.Join(w.dir, "index.json"), indexData, 0o644)
}

type ociReader struct {
	config ociConfig
	layers map[string]map[string][]byte
}

func (r *ociReader) Get(_ context.Context, key string) ([]byte, io.ReadCloser, error) {
	ref, ok := r.config.Index[key]
	if !ok {
		return nil, nil, fmt.Errorf("key not found in OCI archive: %s", key)
	}

	layer, ok := r.layers[ref.LayerDigest]
	if !ok {
		return nil, nil, fmt.Errorf("layer not found: %s", ref.LayerDigest)
	}

	meta, ok := layer[ref.MetaName]
	if !ok {
		return nil, nil, fmt.Errorf("meta not found: %s", ref.MetaName)
	}
	body, ok := layer[ref.BodyName]
	if !ok {
		return nil, nil, fmt.Errorf("body not found: %s", ref.BodyName)
	}

	return meta, io.NopCloser(bytes.NewReader(body)), nil
}

func (r *ociReader) List(_ context.Context) ([]string, error) {
	keys := make([]string, 0, len(r.config.Index))
	for k := range r.config.Index {
		keys = append(keys, k)
	}
	return keys, nil
}

func (r *ociReader) Close() error {
	return nil
}

// helpers

func writeBlob(dir, digest string, data []byte) error {
	// digest is "sha256:hex"
	hex := digest[7:] // strip "sha256:"
	path := filepath.Join(dir, "blobs", "sha256", hex)
	return os.WriteFile(path, data, 0o644)
}

func readBlob(dir, digest string) ([]byte, error) {
	hex := digest[7:]
	path := filepath.Join(dir, "blobs", "sha256", hex)
	return os.ReadFile(path)
}

func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func readTarBytes(data []byte) (map[string][]byte, error) {
	tr := tar.NewReader(bytes.NewReader(data))
	files := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[hdr.Name] = content
	}
	return files, nil
}
```

- [ ] **Step 4: Create OCI registry push/pull stub**

Create `internal/archive/oci_registry.go`:

```go
package archive

import (
	"context"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
)

func PushToRegistry(ctx context.Context, layoutDir, ref string) error {
	store, err := oci.New(layoutDir)
	if err != nil {
		return fmt.Errorf("opening OCI layout: %w", err)
	}

	repo, err := remote.NewRepository(ref)
	if err != nil {
		return fmt.Errorf("creating remote repo: %w", err)
	}

	desc, err := store.Resolve(ctx, store.Reference)
	if err != nil {
		return fmt.Errorf("resolving local manifest: %w", err)
	}

	if _, err := oras.Copy(ctx, store, desc.Digest.String(), repo, ref, oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("pushing to registry: %w", err)
	}

	return nil
}

func PullFromRegistry(ctx context.Context, ref, destDir string) error {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return fmt.Errorf("creating remote repo: %w", err)
	}

	store, err := oci.New(destDir)
	if err != nil {
		return fmt.Errorf("creating OCI layout: %w", err)
	}

	desc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return fmt.Errorf("resolving remote ref: %w", err)
	}

	if _, err := oras.Copy(ctx, repo, desc.Digest.String(), store, "", oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("pulling from registry: %w", err)
	}

	return nil
}
```

Note: The `oci_registry.go` file uses `oras-go` and `opencontainers/image-spec`. The exact API may need adjustment based on the latest `oras-go/v2` version. The compiler will guide us during build.

- [ ] **Step 5: Install OCI dependencies and run tests**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go get oras.land/oras-go/v2
go get github.com/opencontainers/image-spec
go test ./internal/archive/ -v -run TestOCI
```

Expected: tests PASS (registry tests are not run here — they need a real registry).

- [ ] **Step 6: Commit**

```bash
git add internal/archive/oci.go internal/archive/oci_registry.go internal/archive/oci_test.go
git commit -m "feat: add OCI archive format with layer grouping and registry push/pull"
```

---

### Task 10: Archive Format Detection

**Files:**
- Create: `internal/archive/detect.go`
- Create: `internal/archive/detect_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/archive/detect_test.go`:

```go
package archive_test

import (
	"testing"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		dest   string
		want   string
	}{
		{"./output.tar.gz", "tgz"},
		{"./output.tgz", "tgz"},
		{"/tmp/archive.tar.gz", "tgz"},
		{"./output/", "cas"},
		{"/tmp/mydir", "cas"},
		{"registry.example.com/repo:tag", "oci"},
		{"ghcr.io/org/repo:v1", "oci"},
		{"localhost:5000/test:latest", "oci"},
	}

	for _, tt := range tests {
		got := archive.DetectFormat(tt.dest)
		if got != tt.want {
			t.Errorf("DetectFormat(%q) = %q, want %q", tt.dest, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/archive/ -v -run TestDetectFormat
```

Expected: FAIL — `DetectFormat` not defined.

- [ ] **Step 3: Implement format detection**

Create `internal/archive/detect.go`:

```go
package archive

import (
	"strings"
)

func DetectFormat(dest string) string {
	if strings.HasSuffix(dest, ".tar.gz") || strings.HasSuffix(dest, ".tgz") {
		return "tgz"
	}

	// If it contains a colon (not a Windows drive) and/or a slash with a dot before it,
	// it's likely a registry reference
	if looksLikeRegistryRef(dest) {
		return "oci"
	}

	// Default to CAS (directory-based)
	return "cas"
}

func looksLikeRegistryRef(s string) bool {
	// Registry refs look like: host/repo:tag or host:port/repo:tag
	// They contain at least one slash and usually a colon for the tag
	// Local paths start with /, ./, or ../ or have no slashes
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return false
	}
	// Must contain a slash (host/repo)
	if !strings.Contains(s, "/") {
		return false
	}
	return true
}

func NewFormat(name string, ociEntriesPerLayer int) Format {
	switch name {
	case "tgz":
		return &TarGzFormat{}
	case "oci":
		return &OCIFormat{EntriesPerLayer: ociEntriesPerLayer}
	case "cas":
		return &CASFormat{}
	default:
		return &TarGzFormat{}
	}
}

func NewFormatFromDest(dest string, ociEntriesPerLayer int) Format {
	return NewFormat(DetectFormat(dest), ociEntriesPerLayer)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/archive/ -v -run TestDetectFormat
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/detect.go internal/archive/detect_test.go
git commit -m "feat: add archive format auto-detection from destination path"
```

---

### Task 11: MITM Proxy Engine & Request Handler

**Files:**
- Create: `internal/proxy/proxy.go`
- Create: `internal/proxy/handler.go`

- [ ] **Step 1: Implement the proxy engine**

Create `internal/proxy/proxy.go`:

```go
package proxy

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

type Mode int

const (
	ModeServe   Mode = iota
	ModeRecord
	ModeOffline
)

type Config struct {
	Mode            Mode
	Cache           *cache.Cache
	CertCache       *tlspkg.CertCache
	CA              *tlspkg.CA
	KeyHeaders      []string
	UpstreamTimeout time.Duration
	Logger          *slog.Logger

	AllowFallback bool
}

func New(cfg *Config) *goproxy.ProxyHttpServer {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = cfg.Logger != nil && cfg.Logger.Enabled(nil, slog.LevelDebug)

	// Configure MITM with our CA
	tlsCfg := func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
		cert, err := cfg.CertCache.GetOrCreate(host)
		if err != nil {
			return nil, err
		}
		return &tls.Config{
			Certificates: []tls.Certificate{*cert},
		}, nil
	}

	proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			return &goproxy.ConnectAction{
				Action:    goproxy.ConnectMitm,
				TLSConfig: tlsCfg,
			}, host
		},
	))

	handler := &Handler{
		cache:      cfg.Cache,
		keyHeaders: cfg.KeyHeaders,
		mode:       cfg.Mode,
		logger:     cfg.Logger,
		timeout:    cfg.UpstreamTimeout,
	}

	proxy.OnRequest().DoFunc(handler.HandleRequest)
	proxy.OnResponse().DoFunc(handler.HandleResponse)

	return proxy
}
```

- [ ] **Step 2: Implement the request/response handler**

Create `internal/proxy/handler.go`:

```go
package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"golang.org/x/sync/singleflight"
)

type Handler struct {
	cache      *cache.Cache
	keyHeaders []string
	mode       Mode
	logger     *slog.Logger
	timeout    time.Duration
	group      singleflight.Group
}

type contextKey string

const cacheKeyCtx contextKey = "cacheKey"

func (h *Handler) HandleRequest(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	// Compute cache key
	key := ComputeCacheKey(req, h.keyHeaders)
	ctx.UserData = key

	h.logger.Debug("request", "method", req.Method, "url", req.URL.String(), "cache_key", key)

	// Try cache
	meta, bodyRC, err := h.cache.Get(req.Context(), key)
	if err == nil {
		defer bodyRC.Close()
		bodyBytes, _ := io.ReadAll(bodyRC)
		h.logger.Info("cache hit", "url", req.URL.String(), "key", key)
		return req, buildResponse(req, meta, bodyBytes)
	}

	if h.mode == ModeOffline {
		h.logger.Info("cache miss (offline)", "url", req.URL.String(), "key", key)
		return req, goproxy.NewResponse(req, "text/plain", http.StatusBadGateway,
			"escrow-proxy: cache miss in offline mode for "+req.URL.String())
	}

	// Cache miss — let request proceed to upstream
	h.logger.Info("cache miss", "url", req.URL.String(), "key", key)
	return req, nil
}

func (h *Handler) HandleResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if resp == nil || ctx.UserData == nil {
		return resp
	}

	key, ok := ctx.UserData.(string)
	if !ok {
		return resp
	}

	// Only cache 2xx and 3xx
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		h.logger.Debug("not caching", "status", resp.StatusCode, "url", ctx.Req.URL.String())
		return resp
	}

	// Read body, store, then return a new body reader
	bodyBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		h.logger.Error("reading response body", "error", err)
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		return resp
	}

	meta := &cache.EntryMeta{
		Method:     ctx.Req.Method,
		URL:        ctx.Req.URL.String(),
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
	}

	bgCtx := context.Background()
	if err := h.cache.Put(bgCtx, key, meta, bytes.NewReader(bodyBytes)); err != nil {
		h.logger.Warn("failed to cache response", "error", err, "url", ctx.Req.URL.String())
	} else {
		h.logger.Info("cached", "url", ctx.Req.URL.String(), "key", key, "status", resp.StatusCode)
	}

	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return resp
}

func buildResponse(req *http.Request, meta *cache.EntryMeta, body []byte) *http.Response {
	resp := &http.Response{
		StatusCode:    meta.StatusCode,
		Status:        http.StatusText(meta.StatusCode),
		Header:        meta.Header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
	}
	return resp
}
```

- [ ] **Step 3: Install goproxy and singleflight dependencies**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go get github.com/elazarl/goproxy
go get golang.org/x/sync/singleflight
```

- [ ] **Step 4: Verify build compiles**

```bash
go build ./internal/proxy/
```

Expected: compiles without errors.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/handler.go
git commit -m "feat: add MITM proxy engine with cache-aware request handler"
```

---

### Task 12: Wire Up CLI Subcommands

**Files:**
- Modify: `cmd/escrow-proxy/main.go`

- [ ] **Step 1: Update main.go to wire everything together**

Replace `cmd/escrow-proxy/main.go` with:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/loopingz/escrow-proxy/internal/archive"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/config"
	"github.com/loopingz/escrow-proxy/internal/proxy"
	"github.com/loopingz/escrow-proxy/internal/storage"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "escrow-proxy",
		Short: "MITM caching proxy for CI/CD dependency caching",
	}

	rootCmd.PersistentFlags().String("config", "", "path to config file (YAML)")
	rootCmd.PersistentFlags().StringP("listen", "l", ":8080", "bind address")
	rootCmd.PersistentFlags().String("ca-cert", "", "path to CA certificate")
	rootCmd.PersistentFlags().String("ca-key", "", "path to CA private key")
	rootCmd.PersistentFlags().String("cache-key-headers", "Accept,Accept-Encoding", "headers to include in cache key")
	rootCmd.PersistentFlags().String("log-level", "info", "log level: debug, info, warn, error")
	rootCmd.PersistentFlags().String("storage", "local", "comma-separated storage tier list")
	rootCmd.PersistentFlags().String("local-dir", "", "local cache directory")
	rootCmd.PersistentFlags().String("gcs-bucket", "", "GCS bucket name")
	rootCmd.PersistentFlags().String("gcs-prefix", "", "GCS key prefix")
	rootCmd.PersistentFlags().String("s3-bucket", "", "S3 bucket name")
	rootCmd.PersistentFlags().String("s3-prefix", "", "S3 key prefix")
	rootCmd.PersistentFlags().String("s3-region", "", "S3 region")
	rootCmd.PersistentFlags().Duration("upstream-timeout", 30*time.Second, "upstream request timeout")

	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newRecordCmd())
	rootCmd.AddCommand(newOfflineCmd())
	rootCmd.AddCommand(newCACmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	cfgPath, _ := cmd.Flags().GetString("config")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	// CLI overrides
	if v, _ := cmd.Flags().GetString("listen"); cmd.Flags().Changed("listen") {
		cfg.Listen = v
	}
	if v, _ := cmd.Flags().GetString("ca-cert"); v != "" {
		cfg.CA.Cert = v
	}
	if v, _ := cmd.Flags().GetString("ca-key"); v != "" {
		cfg.CA.Key = v
	}
	if v, _ := cmd.Flags().GetString("cache-key-headers"); cmd.Flags().Changed("cache-key-headers") {
		cfg.Cache.KeyHeaders = strings.Split(v, ",")
	}
	if v, _ := cmd.Flags().GetString("log-level"); cmd.Flags().Changed("log-level") {
		cfg.LogLevel = v
	}
	if v, _ := cmd.Flags().GetDuration("upstream-timeout"); cmd.Flags().Changed("upstream-timeout") {
		cfg.UpstreamTimeout = v
	}

	return cfg, nil
}

func buildStorage(cfg *config.Config) (storage.Storage, error) {
	var tiers []storage.Storage
	for _, tc := range cfg.Storage.Tiers {
		switch tc.Type {
		case "local":
			dir := tc.Dir
			if dir == "" {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, ".escrow-proxy", "cache")
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("creating local dir: %w", err)
			}
			tiers = append(tiers, storage.NewLocal(dir))
		case "gcs":
			s, err := storage.NewGCS(context.Background(), tc.Bucket, tc.Prefix)
			if err != nil {
				return nil, fmt.Errorf("creating GCS storage: %w", err)
			}
			tiers = append(tiers, s)
		case "s3":
			s, err := storage.NewS3(context.Background(), tc.Bucket, tc.Prefix, tc.Region)
			if err != nil {
				return nil, fmt.Errorf("creating S3 storage: %w", err)
			}
			tiers = append(tiers, s)
		default:
			return nil, fmt.Errorf("unknown storage type: %s", tc.Type)
		}
	}
	if len(tiers) == 1 {
		return tiers[0], nil
	}
	return storage.NewTiered(tiers), nil
}

func setupLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func startProxy(p http.Handler, listen string, logger *slog.Logger) {
	srv := &http.Server{Addr: listen, Handler: p}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Info("starting proxy", "listen", listen)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-sigCh
	logger.Info("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the caching proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)

			homeDir, _ := os.UserHomeDir()
			caDir := filepath.Join(homeDir, ".escrow-proxy", "ca")
			ca, err := tlspkg.EnsureCA(caDir, cfg.CA.Cert, cfg.CA.Key)
			if err != nil {
				return fmt.Errorf("setting up CA: %w", err)
			}
			logger.Info("CA certificate", "path", filepath.Join(caDir, "ca.crt"))

			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}

			c := cache.New(store)
			certCache := tlspkg.NewCertCache(ca, 1000)

			p := proxy.New(&proxy.Config{
				Mode:            proxy.ModeServe,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
				UpstreamTimeout: cfg.UpstreamTimeout,
				Logger:          logger,
			})

			startProxy(p, cfg.Listen, logger)
			return nil
		},
	}
}

func newRecordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Start the caching proxy in record mode",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)

			// Record-specific flags
			if v, _ := cmd.Flags().GetString("output"); v != "" {
				cfg.Record.Output = v
			}
			if v, _ := cmd.Flags().GetString("format"); v != "" {
				cfg.Record.Format = v
			}
			if v, _ := cmd.Flags().GetInt("oci-entries-per-layer"); cmd.Flags().Changed("oci-entries-per-layer") {
				cfg.Record.OCIEntriesPerLayer = v
			}

			if cfg.Record.Output == "" {
				return fmt.Errorf("--output is required in record mode")
			}

			homeDir, _ := os.UserHomeDir()
			caDir := filepath.Join(homeDir, ".escrow-proxy", "ca")
			ca, err := tlspkg.EnsureCA(caDir, cfg.CA.Cert, cfg.CA.Key)
			if err != nil {
				return fmt.Errorf("setting up CA: %w", err)
			}

			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}

			c := cache.New(store)
			certCache := tlspkg.NewCertCache(ca, 1000)

			// Setup archive writer
			formatName := cfg.Record.Format
			if formatName == "" {
				formatName = archive.DetectFormat(cfg.Record.Output)
			}
			archiveFormat := archive.NewFormat(formatName, cfg.Record.OCIEntriesPerLayer)
			archiveWriter, err := archiveFormat.NewWriter(cfg.Record.Output)
			if err != nil {
				return fmt.Errorf("creating archive writer: %w", err)
			}

			recorder := cache.NewRecorder(c, archiveWriter)

			p := proxy.New(&proxy.Config{
				Mode:            proxy.ModeRecord,
				Cache:           recorder.Cache(),
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
				UpstreamTimeout: cfg.UpstreamTimeout,
				Logger:          logger,
			})

			// Start proxy (blocks until signal)
			startProxy(p, cfg.Listen, logger)

			// Finalize archive
			logger.Info("finalizing archive", "output", cfg.Record.Output)
			if err := recorder.Finalize(); err != nil {
				return fmt.Errorf("finalizing archive: %w", err)
			}
			logger.Info("archive written", "output", cfg.Record.Output)

			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "", "archive destination (path or registry ref)")
	cmd.Flags().String("format", "", "archive format: tgz, oci, cas (auto-detect if empty)")
	cmd.Flags().Int("oci-entries-per-layer", 1000, "entries per OCI layer")
	return cmd
}

func newOfflineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "offline",
		Short: "Serve only from an archive",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)

			if v, _ := cmd.Flags().GetString("archive"); v != "" {
				cfg.Offline.Archive = v
			}
			if v, _ := cmd.Flags().GetBool("allow-fallback"); cmd.Flags().Changed("allow-fallback") {
				cfg.Offline.AllowFallback = v
			}

			if cfg.Offline.Archive == "" {
				return fmt.Errorf("--archive is required in offline mode")
			}

			homeDir, _ := os.UserHomeDir()
			caDir := filepath.Join(homeDir, ".escrow-proxy", "ca")
			ca, err := tlspkg.EnsureCA(caDir, cfg.CA.Cert, cfg.CA.Key)
			if err != nil {
				return fmt.Errorf("setting up CA: %w", err)
			}

			// Load archive
			formatName := archive.DetectFormat(cfg.Offline.Archive)
			archiveFormat := archive.NewFormat(formatName, 1000)
			archiveReader, err := archiveFormat.NewReader(cfg.Offline.Archive)
			if err != nil {
				return fmt.Errorf("loading archive: %w", err)
			}
			defer archiveReader.Close()

			// Create archive-backed storage
			archiveStore := cache.NewArchiveStorage(archiveReader)

			var store storage.Storage
			if cfg.Offline.AllowFallback {
				realStore, err := buildStorage(cfg)
				if err != nil {
					return err
				}
				store = storage.NewTiered([]storage.Storage{archiveStore, realStore})
			} else {
				store = archiveStore
			}

			c := cache.New(store)
			certCache := tlspkg.NewCertCache(ca, 1000)

			mode := proxy.ModeOffline
			if cfg.Offline.AllowFallback {
				mode = proxy.ModeServe
			}

			p := proxy.New(&proxy.Config{
				Mode:            mode,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
				UpstreamTimeout: cfg.UpstreamTimeout,
				Logger:          logger,
			})

			startProxy(p, cfg.Listen, logger)
			return nil
		},
	}
	cmd.Flags().StringP("archive", "a", "", "archive source (path or registry ref)")
	cmd.Flags().Bool("allow-fallback", false, "on cache miss, forward upstream instead of 502")
	return cmd
}

func newCACmd() *cobra.Command {
	caCmd := &cobra.Command{
		Use:   "ca",
		Short: "CA certificate management",
	}
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Print CA certificate PEM to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			homeDir, _ := os.UserHomeDir()
			caDir := filepath.Join(homeDir, ".escrow-proxy", "ca")
			ca, err := tlspkg.EnsureCA(caDir, cfg.CA.Cert, cfg.CA.Key)
			if err != nil {
				return fmt.Errorf("loading CA: %w", err)
			}
			fmt.Print(string(tlspkg.ExportCAPEM(ca)))
			return nil
		},
	}
	caCmd.AddCommand(exportCmd)
	return caCmd
}
```

- [ ] **Step 2: Add the Recorder and ArchiveStorage types to cache package**

These are referenced by main.go but don't exist yet. Create them.

Add to `internal/cache/cache.go` (append to the file):

```go
// Recorder wraps a Cache and records all entries for archiving
type Recorder struct {
	cache   *Cache
	writer  archive.Writer
	mu      sync.Mutex
	keys    []string
}

func NewRecorder(c *Cache, w archive.Writer) *Recorder {
	return &Recorder{cache: c, writer: w}
}

func (r *Recorder) Cache() *Cache {
	return &Cache{storage: &recordingStorage{
		inner:    r.cache.storage,
		recorder: r,
	}}
}

func (r *Recorder) recordEntry(ctx context.Context, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
}

func (r *Recorder) Finalize() error {
	r.mu.Lock()
	keys := make([]string, len(r.keys))
	copy(keys, r.keys)
	r.mu.Unlock()

	ctx := context.Background()
	seen := make(map[string]bool)
	for _, key := range keys {
		baseKey := key
		if strings.HasSuffix(key, ".meta") {
			baseKey = key[:len(key)-5]
		} else if strings.HasSuffix(key, ".body") {
			baseKey = key[:len(key)-5]
		}
		if seen[baseKey] {
			continue
		}
		seen[baseKey] = true

		metaRC, err := r.cache.storage.Get(ctx, metaKey(baseKey))
		if err != nil {
			continue
		}
		metaBytes, _ := io.ReadAll(metaRC)
		metaRC.Close()

		bodyRC, err := r.cache.storage.Get(ctx, bodyKey(baseKey))
		if err != nil {
			continue
		}

		if err := r.writer.Add(ctx, baseKey, metaBytes, bodyRC); err != nil {
			bodyRC.Close()
			return fmt.Errorf("adding %s to archive: %w", baseKey, err)
		}
		bodyRC.Close()
	}

	return r.writer.Close()
}

type recordingStorage struct {
	inner    storage.Storage
	recorder *Recorder
}

func (s *recordingStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.inner.Get(ctx, key)
}

func (s *recordingStorage) Put(ctx context.Context, key string, r io.Reader) error {
	err := s.inner.Put(ctx, key, r)
	if err == nil {
		s.recorder.recordEntry(ctx, key)
	}
	return err
}

func (s *recordingStorage) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *recordingStorage) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *recordingStorage) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}
```

Update the imports at the top of `internal/cache/cache.go` to include:

```go
import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/loopingz/escrow-proxy/internal/archive"
	"github.com/loopingz/escrow-proxy/internal/storage"
)
```

- [ ] **Step 3: Add ArchiveStorage to cache package**

Append to `internal/cache/cache.go`:

```go
// ArchiveStorage wraps an archive.Reader as a storage.Storage for offline mode
type ArchiveStorage struct {
	reader archive.Reader
}

func NewArchiveStorage(r archive.Reader) *ArchiveStorage {
	return &ArchiveStorage{reader: r}
}

func (s *ArchiveStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	// The key will be like "abc123.meta" or "abc123.body"
	// We need to extract the base key and the suffix
	var baseKey, suffix string
	if strings.HasSuffix(key, ".meta") {
		baseKey = key[:len(key)-5]
		suffix = "meta"
	} else if strings.HasSuffix(key, ".body") {
		baseKey = key[:len(key)-5]
		suffix = "body"
	} else {
		return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}

	meta, body, err := s.reader.Get(ctx, baseKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}

	if suffix == "meta" {
		body.Close()
		return io.NopCloser(bytes.NewReader(meta)), nil
	}
	return body, nil
}

func (s *ArchiveStorage) Put(_ context.Context, _ string, _ io.Reader) error {
	return fmt.Errorf("archive storage is read-only")
}

func (s *ArchiveStorage) Exists(ctx context.Context, key string) (bool, error) {
	rc, err := s.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	rc.Close()
	return true, nil
}

func (s *ArchiveStorage) Delete(_ context.Context, _ string) error {
	return fmt.Errorf("archive storage is read-only")
}

func (s *ArchiveStorage) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.reader.List(ctx)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			result = append(result, k+".meta", k+".body")
		}
	}
	return result, nil
}
```

- [ ] **Step 4: Verify the full project builds**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go mod tidy
go build ./cmd/escrow-proxy/
```

Expected: compiles without errors.

- [ ] **Step 5: Test the CLI help output**

```bash
./escrow-proxy --help
./escrow-proxy serve --help
./escrow-proxy record --help
./escrow-proxy offline --help
./escrow-proxy ca export
```

Expected: proper help output for all commands. `ca export` prints a PEM certificate.

- [ ] **Step 6: Commit**

```bash
git add cmd/escrow-proxy/main.go internal/cache/cache.go go.mod go.sum
git commit -m "feat: wire up CLI subcommands with proxy, cache, storage, and archive layers"
```

---

### Task 13: GCS Storage Backend

**Files:**
- Create: `internal/storage/gcs.go`
- Create: `internal/storage/gcs_test.go`

- [ ] **Step 1: Write failing tests for GCS storage**

Create `internal/storage/gcs_test.go`:

```go
//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestGCS_PutAndGet(t *testing.T) {
	bucket := os.Getenv("TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("TEST_GCS_BUCKET not set")
	}

	s, err := storage.NewGCS(context.Background(), bucket, "test-prefix/")
	if err != nil {
		t.Fatalf("NewGCS: %v", err)
	}
	ctx := context.Background()

	data := []byte("hello gcs")
	if err := s.Put(ctx, "test-key", bytes.NewReader(data)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	defer s.Delete(ctx, "test-key")

	rc, err := s.Get(ctx, "test-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q, want %q", got, data)
	}
}
```

- [ ] **Step 2: Implement GCS storage**

Create `internal/storage/gcs.go`:

```go
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
```

- [ ] **Step 3: Install GCS dependency and verify build**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go get cloud.google.com/go/storage
go get google.golang.org/api/iterator
go build ./internal/storage/
```

Expected: compiles.

- [ ] **Step 4: Commit**

```bash
git add internal/storage/gcs.go internal/storage/gcs_test.go go.mod go.sum
git commit -m "feat: add GCS storage backend"
```

---

### Task 14: S3 Storage Backend

**Files:**
- Create: `internal/storage/s3.go`
- Create: `internal/storage/s3_test.go`

- [ ] **Step 1: Write failing tests for S3 storage**

Create `internal/storage/s3_test.go`:

```go
//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

func TestS3_PutAndGet(t *testing.T) {
	bucket := os.Getenv("TEST_S3_BUCKET")
	region := os.Getenv("TEST_S3_REGION")
	if bucket == "" || region == "" {
		t.Skip("TEST_S3_BUCKET or TEST_S3_REGION not set")
	}

	s, err := storage.NewS3(context.Background(), bucket, "test-prefix/", region)
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	ctx := context.Background()

	data := []byte("hello s3")
	if err := s.Put(ctx, "test-key", bytes.NewReader(data)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	defer s.Delete(ctx, "test-key")

	rc, err := s.Get(ctx, "test-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q, want %q", got, data)
	}
}
```

- [ ] **Step 2: Implement S3 storage**

Create `internal/storage/s3.go`:

```go
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3 struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3(ctx context.Context, bucket, prefix, region string) (*S3, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return &S3{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *S3) objectKey(key string) string {
	return s.prefix + key
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("getting %s from S3: %w", key, err)
	}
	return out.Body, nil
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading data: %w", err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("putting %s to S3: %w", key, err)
	}
	return nil
}

func (s *S3) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
	})
	if err != nil {
		var nsk *types.NotFound
		if errors.As(err, &nsk) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s in S3: %w", key, err)
	}
	return true, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
	})
	if err != nil {
		return fmt.Errorf("deleting %s from S3: %w", key, err)
	}
	return nil
}

func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	fullPrefix := s.objectKey(prefix)
	out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(fullPrefix),
	})
	if err != nil {
		return nil, fmt.Errorf("listing S3 objects: %w", err)
	}
	var keys []string
	for _, obj := range out.Contents {
		key := strings.TrimPrefix(*obj.Key, s.prefix)
		keys = append(keys, key)
	}
	return keys, nil
}
```

- [ ] **Step 3: Install S3 dependencies and verify build**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go get github.com/aws/aws-sdk-go-v2
go get github.com/aws/aws-sdk-go-v2/config
go get github.com/aws/aws-sdk-go-v2/service/s3
go build ./internal/storage/
```

Expected: compiles.

- [ ] **Step 4: Commit**

```bash
git add internal/storage/s3.go internal/storage/s3_test.go go.mod go.sum
git commit -m "feat: add S3 storage backend"
```

---

### Task 15: Config Tests

**Files:**
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write config tests**

Create `internal/config/config_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Listen != ":8080" {
		t.Fatalf("Listen: got %s, want :8080", cfg.Listen)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel: got %s, want info", cfg.LogLevel)
	}
	if len(cfg.Cache.KeyHeaders) != 2 {
		t.Fatalf("KeyHeaders: got %d, want 2", len(cfg.Cache.KeyHeaders))
	}
	if cfg.Record.OCIEntriesPerLayer != 1000 {
		t.Fatalf("OCIEntriesPerLayer: got %d, want 1000", cfg.Record.OCIEntriesPerLayer)
	}
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
listen: ":9090"
log_level: "debug"
ca:
  cert: /tmp/ca.crt
  key: /tmp/ca.key
cache:
  key_headers: ["Accept"]
storage:
  tiers:
    - type: local
      dir: /tmp/cache
    - type: gcs
      bucket: my-bucket
      prefix: pfx/
record:
  output: registry.example.com/cache:v1
  format: oci
  oci_entries_per_layer: 500
offline:
  archive: ./archive.tar.gz
  allow_fallback: true
`
	os.WriteFile(cfgPath, []byte(content), 0o644)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":9090" {
		t.Fatalf("Listen: got %s, want :9090", cfg.Listen)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel: got %s, want debug", cfg.LogLevel)
	}
	if cfg.CA.Cert != "/tmp/ca.crt" {
		t.Fatalf("CA.Cert: got %s", cfg.CA.Cert)
	}
	if len(cfg.Storage.Tiers) != 2 {
		t.Fatalf("Storage.Tiers: got %d, want 2", len(cfg.Storage.Tiers))
	}
	if cfg.Storage.Tiers[1].Type != "gcs" {
		t.Fatalf("Tier[1].Type: got %s, want gcs", cfg.Storage.Tiers[1].Type)
	}
	if cfg.Record.OCIEntriesPerLayer != 500 {
		t.Fatalf("OCIEntriesPerLayer: got %d, want 500", cfg.Record.OCIEntriesPerLayer)
	}
	if !cfg.Offline.AllowFallback {
		t.Fatal("AllowFallback: expected true")
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Fatalf("expected default listen, got %s", cfg.Listen)
	}
}
```

- [ ] **Step 2: Run config tests**

```bash
go test ./internal/config/ -v
```

Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/config/config_test.go
git commit -m "feat: add config loading tests"
```

---

### Task 16: Integration Test — Full Round Trip

**Files:**
- Create: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Write integration test**

Create `internal/proxy/proxy_test.go`:

```go
package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/proxy"
	"github.com/loopingz/escrow-proxy/internal/storage"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

func TestProxy_ServeMode_CachesResponse(t *testing.T) {
	// Setup upstream server
	callCount := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("package-data"))
	}))
	defer upstream.Close()

	// Setup proxy
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certCache := tlspkg.NewCertCache(ca, 100)
	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)

	logger := setupTestLogger()

	p := proxy.New(&proxy.Config{
		Mode:       proxy.ModeServe,
		Cache:      c,
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{"Accept"},
		Logger:     logger,
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	// Create client that trusts our CA and upstream's cert
	proxyURL, _ := url.Parse(proxyServer.URL)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	pool.AddCert(upstream.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
			},
		},
	}

	// First request — should hit upstream
	resp, err := client.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "package-data" {
		t.Fatalf("body: got %q", body)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 upstream call, got %d", callCount)
	}

	// Second request — should be served from cache
	resp2, err := client.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body2) != "package-data" {
		t.Fatalf("cached body: got %q", body2)
	}
	if callCount != 1 {
		t.Fatalf("expected still 1 upstream call (cached), got %d", callCount)
	}
}

func TestProxy_RecordAndOffline_RoundTrip(t *testing.T) {
	// Setup upstream
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("recorded-data"))
	}))
	defer upstream.Close()

	ca, _ := tlspkg.GenerateCA()
	certCache := tlspkg.NewCertCache(ca, 100)
	dir := t.TempDir()
	store := storage.NewLocal(filepath.Join(dir, "cache"))
	c := cache.New(store)
	logger := setupTestLogger()

	// Record phase
	archivePath := filepath.Join(dir, "archive.tar.gz")
	archiveFormat := &archive.TarGzFormat{}
	archiveWriter, _ := archiveFormat.NewWriter(archivePath)
	recorder := cache.NewRecorder(c, archiveWriter)

	p := proxy.New(&proxy.Config{
		Mode:       proxy.ModeRecord,
		Cache:      recorder.Cache(),
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{},
		Logger:     logger,
	})

	proxyServer := httptest.NewServer(p)

	proxyURL, _ := url.Parse(proxyServer.URL)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	pool.AddCert(upstream.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
			},
		},
	}

	resp, _ := client.Get(upstream.URL + "/pkg")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "recorded-data" {
		t.Fatalf("record body: got %q", body)
	}

	proxyServer.Close()
	recorder.Finalize()
	upstream.Close()

	// Offline phase
	archiveReader, err := archiveFormat.NewReader(archivePath)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	archiveStore := cache.NewArchiveStorage(archiveReader)
	offlineCache := cache.New(archiveStore)

	p2 := proxy.New(&proxy.Config{
		Mode:       proxy.ModeOffline,
		Cache:      offlineCache,
		CertCache:  certCache,
		CA:         ca,
		KeyHeaders: []string{},
		Logger:     logger,
	})

	proxyServer2 := httptest.NewServer(p2)
	defer proxyServer2.Close()

	proxyURL2, _ := url.Parse(proxyServer2.URL)
	client2 := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL2),
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
			},
		},
	}

	// This should work from archive (no upstream)
	resp2, err := client2.Get(upstream.URL + "/pkg")
	if err != nil {
		t.Fatalf("offline request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body2) != "recorded-data" {
		t.Fatalf("offline body: got %q", body2)
	}
}
```

Note: The integration test needs the `archive` import added. The test also needs a `setupTestLogger` helper:

```go
import (
	"log/slog"
	"os"

	"github.com/loopingz/escrow-proxy/internal/archive"
)

func setupTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
```

- [ ] **Step 2: Run integration tests**

```bash
go test ./internal/proxy/ -v -run TestProxy
```

Expected: tests PASS. The MITM proxy test may need adjustments based on `goproxy`'s exact API — fix any compilation issues.

- [ ] **Step 3: Run all tests**

```bash
go test ./... -v -count=1
```

Expected: all unit tests PASS. Integration tests with build tag `integration` are skipped.

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/proxy_test.go
git commit -m "feat: add proxy integration tests for serve, record, and offline modes"
```

---

### Task 17: Final Build & Smoke Test

**Files:** None new — validation only.

- [ ] **Step 1: Clean build**

```bash
cd /Users/loopingz/Git/loopingz/escrow-proxy
go mod tidy
go build -o escrow-proxy ./cmd/escrow-proxy/
```

- [ ] **Step 2: Run all tests**

```bash
go test ./... -v -count=1
```

Expected: all PASS.

- [ ] **Step 3: Smoke test CLI**

```bash
./escrow-proxy --help
./escrow-proxy ca export
./escrow-proxy serve --help
./escrow-proxy record --help
./escrow-proxy offline --help
```

- [ ] **Step 4: Verify binary works end-to-end**

Start the proxy in one terminal:
```bash
./escrow-proxy serve -l :8080 --log-level debug
```

In another terminal:
```bash
export HTTPS_PROXY=http://localhost:8080
# Trust the CA cert
./escrow-proxy ca export > /tmp/escrow-ca.pem
curl --cacert /tmp/escrow-ca.pem https://httpbin.org/get
# Second request should be cached (check proxy logs)
curl --cacert /tmp/escrow-ca.pem https://httpbin.org/get
```

- [ ] **Step 5: Final commit**

```bash
git add go.mod go.sum
git commit -m "chore: tidy go modules after full implementation"
```
