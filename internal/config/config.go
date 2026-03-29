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
