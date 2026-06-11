package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	Metrics         MetricsConfig `yaml:"metrics"`
	LogLevel        string        `yaml:"log_level"`
	UpstreamTimeout time.Duration `yaml:"upstream_timeout"`
}

type CAConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type CacheConfig struct {
	KeyHeaders      []string `yaml:"key_headers"`
	Methods         []string `yaml:"methods"`
	ExcludePatterns []string `yaml:"exclude_patterns"`
	// RevalidatePatterns match URLs that should be served from cache only
	// while fresh (younger than RevalidateInterval). Stale matches trigger
	// an upstream fetch; if upstream returns 2xx, the cache is refreshed
	// and the new response served. Anything else (3xx/4xx/5xx/network
	// error) serves the cached body as a fallback.
	RevalidatePatterns []string           `yaml:"revalidate_patterns"`
	RevalidateInterval time.Duration      `yaml:"revalidate_interval"`
	Index              CacheIndexConfig   `yaml:"index"`
	VerifyDigest       VerifyDigestConfig `yaml:"verify_digest"`
}

// DefaultRevalidateInterval is used when revalidate_patterns are
// configured but revalidate_interval is not. Anonymous OCI bearer tokens
// last ~1h and PyPI Simple index pages change on every new wheel upload,
// so 5m is a comfortable middle ground between freshness and load.
const DefaultRevalidateInterval = 5 * time.Minute

// VerifyDigestConfig configures SHA256 verification of response bodies
// whose request URL pins content by digest (OCI v2 blob and manifest
// by-digest paths). The proxy never caches a mismatched response and
// always evicts any existing entry under the same key on mismatch.
type VerifyDigestConfig struct {
	// Enabled controls whether digest verification runs. nil = default
	// (true). Set explicitly to false to disable.
	Enabled *bool `yaml:"enabled,omitempty"`
	// OnMismatch governs the client-facing behavior when an upstream
	// response body does not match the URL's claimed digest. "error"
	// (default) returns HTTP 502; "passthrough" forwards the mismatched
	// body to the client (which can run its own integrity check) but
	// still refuses to cache it.
	OnMismatch string `yaml:"on_mismatch,omitempty"`
}

// VerifyDigestMismatchActions enumerates the legal values for
// VerifyDigestConfig.OnMismatch.
const (
	VerifyDigestActionError       = "error"
	VerifyDigestActionPassthrough = "passthrough"
)

type CacheIndexConfig struct {
	Enabled        *bool         `yaml:"enabled,omitempty"` // nil = default (true)
	Path           string        `yaml:"path,omitempty"`
	FlushInterval  time.Duration `yaml:"flush_interval,omitempty"`
	FlushThreshold int           `yaml:"flush_threshold,omitempty"`
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

// MetricsConfig configures the Prometheus metrics HTTP server. Listen is
// the bind address (default ":9090"); an empty string disables the
// metrics server entirely.
type MetricsConfig struct {
	Listen string `yaml:"listen"`
}

func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		Listen:          ":8080",
		LogLevel:        "info",
		UpstreamTimeout: 30 * time.Second,
		Cache: CacheConfig{
			KeyHeaders: []string{"Accept", "Accept-Encoding"},
			Methods:    []string{"GET", "HEAD"},
			VerifyDigest: VerifyDigestConfig{
				OnMismatch: VerifyDigestActionError,
			},
		},
		Storage: StorageConfig{
			Tiers: []StorageTierConfig{
				{Type: "local", Dir: filepath.Join(homeDir, ".escrow-proxy", "cache")},
			},
		},
		Record: RecordConfig{
			OCIEntriesPerLayer: 1000,
		},
		Metrics: MetricsConfig{
			Listen: ":9090",
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
	for _, p := range cfg.Cache.ExcludePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return nil, fmt.Errorf("invalid cache.exclude_patterns entry %q: %w", p, err)
		}
	}
	for _, p := range cfg.Cache.RevalidatePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return nil, fmt.Errorf("invalid cache.revalidate_patterns entry %q: %w", p, err)
		}
	}
	if len(cfg.Cache.RevalidatePatterns) > 0 && cfg.Cache.RevalidateInterval <= 0 {
		cfg.Cache.RevalidateInterval = DefaultRevalidateInterval
	}
	switch cfg.Cache.VerifyDigest.OnMismatch {
	case "", VerifyDigestActionError, VerifyDigestActionPassthrough:
	default:
		return nil, fmt.Errorf("invalid cache.verify_digest.on_mismatch %q: must be %q or %q",
			cfg.Cache.VerifyDigest.OnMismatch, VerifyDigestActionError, VerifyDigestActionPassthrough)
	}
	return cfg, nil
}
