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
	wantMethods := map[string]bool{"GET": true, "HEAD": true}
	if len(cfg.Cache.Methods) != len(wantMethods) {
		t.Fatalf("Methods: got %v, want GET,HEAD", cfg.Cache.Methods)
	}
	for _, m := range cfg.Cache.Methods {
		if !wantMethods[m] {
			t.Fatalf("Methods: unexpected %q", m)
		}
	}
	if len(cfg.Cache.ExcludePatterns) != 0 {
		t.Fatalf("ExcludePatterns default: got %v, want empty", cfg.Cache.ExcludePatterns)
	}
	if cfg.Record.OCIEntriesPerLayer != 1000 {
		t.Fatalf("OCIEntriesPerLayer: got %d, want 1000", cfg.Record.OCIEntriesPerLayer)
	}
}

func TestLoad_CacheMethodsAndExcludePatterns(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
cache:
  methods: ["GET", "POST"]
  exclude_patterns:
    - '^https://example\.com/login$'
    - '/healthz$'
`
	os.WriteFile(cfgPath, []byte(content), 0o644)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Cache.Methods) != 2 || cfg.Cache.Methods[0] != "GET" || cfg.Cache.Methods[1] != "POST" {
		t.Fatalf("Methods: got %v", cfg.Cache.Methods)
	}
	if len(cfg.Cache.ExcludePatterns) != 2 {
		t.Fatalf("ExcludePatterns: got %v", cfg.Cache.ExcludePatterns)
	}
}

func TestLoad_InvalidExcludePattern(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
cache:
  exclude_patterns:
    - '[invalid('
`
	os.WriteFile(cfgPath, []byte(content), 0o644)

	if _, err := config.Load(cfgPath); err == nil {
		t.Fatal("expected error for invalid regex, got nil")
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
