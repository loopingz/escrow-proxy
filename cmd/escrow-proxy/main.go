package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/loopingz/escrow-proxy/internal/archive"
	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/config"
	"github.com/loopingz/escrow-proxy/internal/index"
	"github.com/loopingz/escrow-proxy/internal/proxy"
	"github.com/loopingz/escrow-proxy/internal/storage"
	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
	"github.com/spf13/cobra"
)

// Build-time metadata populated via ldflags by GoReleaser. Defaults keep
// `go run` and local `go build` usable without any flags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "escrow-proxy",
		Short:   "MITM caching proxy for CI/CD dependency caching",
		Version: buildVersion(version, commit, date),
	}
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	rootCmd.PersistentFlags().String("config", "", "path to config file (YAML); falls back to $ESCROW_PROXY_CONFIG")
	rootCmd.PersistentFlags().StringP("listen", "l", ":8080", "bind address")
	rootCmd.PersistentFlags().String("ca-cert", "", "path to CA certificate")
	rootCmd.PersistentFlags().String("ca-key", "", "path to CA private key")
	rootCmd.PersistentFlags().String("cache-key-headers", "Accept,Accept-Encoding", "headers to include in cache key")
	rootCmd.PersistentFlags().String("log-level", "info", "log level: debug, info, warn, error; falls back to $ESCROW_PROXY_LOG_LEVEL")
	rootCmd.PersistentFlags().String("storage", "local", "comma-separated storage tier list (e.g., local,gcs)")
	rootCmd.PersistentFlags().String("local-dir", "", "local cache directory (default: ~/.escrow-proxy/cache/)")
	rootCmd.PersistentFlags().String("gcs-bucket", "", "GCS bucket name")
	rootCmd.PersistentFlags().String("gcs-prefix", "", "GCS key prefix")
	rootCmd.PersistentFlags().String("s3-bucket", "", "S3 bucket name")
	rootCmd.PersistentFlags().String("s3-prefix", "", "S3 key prefix")
	rootCmd.PersistentFlags().String("s3-region", "", "S3 region")
	rootCmd.PersistentFlags().Duration("upstream-timeout", 30*time.Second, "upstream request timeout")
	rootCmd.PersistentFlags().String("index-db", "", "path to local SQLite index DB (default: <local-dir>/index.db)")
	rootCmd.PersistentFlags().Bool("no-index", false, "disable the SQLite index entirely")

	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newRecordCmd())
	rootCmd.AddCommand(newOfflineCmd())
	rootCmd.AddCommand(newCACmd())
	rootCmd.AddCommand(newCacheCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// loadConfig loads the YAML config file (if any) and applies CLI flag overrides.
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	cfgPath, _ := cmd.Flags().GetString("config")
	if cfgPath == "" {
		cfgPath = os.Getenv("ESCROW_PROXY_CONFIG")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	if cmd.Flags().Changed("listen") {
		cfg.Listen, _ = cmd.Flags().GetString("listen")
	}
	if cmd.Flags().Changed("ca-cert") {
		cfg.CA.Cert, _ = cmd.Flags().GetString("ca-cert")
	}
	if cmd.Flags().Changed("ca-key") {
		cfg.CA.Key, _ = cmd.Flags().GetString("ca-key")
	}
	if cmd.Flags().Changed("cache-key-headers") {
		hdr, _ := cmd.Flags().GetString("cache-key-headers")
		cfg.Cache.KeyHeaders = strings.Split(hdr, ",")
	}
	if cmd.Flags().Changed("log-level") {
		cfg.LogLevel, _ = cmd.Flags().GetString("log-level")
	} else if v := os.Getenv("ESCROW_PROXY_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if cmd.Flags().Changed("upstream-timeout") {
		cfg.UpstreamTimeout, _ = cmd.Flags().GetDuration("upstream-timeout")
	}
	if cmd.Flags().Changed("storage") {
		tierStr, _ := cmd.Flags().GetString("storage")
		tierNames := strings.Split(tierStr, ",")
		var tiers []config.StorageTierConfig
		for _, name := range tierNames {
			t := config.StorageTierConfig{Type: strings.TrimSpace(name)}
			tiers = append(tiers, t)
		}
		cfg.Storage.Tiers = tiers
	}
	if cmd.Flags().Changed("local-dir") {
		dir, _ := cmd.Flags().GetString("local-dir")
		for i := range cfg.Storage.Tiers {
			if cfg.Storage.Tiers[i].Type == "local" {
				cfg.Storage.Tiers[i].Dir = dir
			}
		}
	}
	if cmd.Flags().Changed("gcs-bucket") {
		bucket, _ := cmd.Flags().GetString("gcs-bucket")
		for i := range cfg.Storage.Tiers {
			if cfg.Storage.Tiers[i].Type == "gcs" {
				cfg.Storage.Tiers[i].Bucket = bucket
			}
		}
	}
	if cmd.Flags().Changed("gcs-prefix") {
		prefix, _ := cmd.Flags().GetString("gcs-prefix")
		for i := range cfg.Storage.Tiers {
			if cfg.Storage.Tiers[i].Type == "gcs" {
				cfg.Storage.Tiers[i].Prefix = prefix
			}
		}
	}
	if cmd.Flags().Changed("s3-bucket") {
		bucket, _ := cmd.Flags().GetString("s3-bucket")
		for i := range cfg.Storage.Tiers {
			if cfg.Storage.Tiers[i].Type == "s3" {
				cfg.Storage.Tiers[i].Bucket = bucket
			}
		}
	}
	if cmd.Flags().Changed("s3-prefix") {
		prefix, _ := cmd.Flags().GetString("s3-prefix")
		for i := range cfg.Storage.Tiers {
			if cfg.Storage.Tiers[i].Type == "s3" {
				cfg.Storage.Tiers[i].Prefix = prefix
			}
		}
	}
	if cmd.Flags().Changed("s3-region") {
		region, _ := cmd.Flags().GetString("s3-region")
		for i := range cfg.Storage.Tiers {
			if cfg.Storage.Tiers[i].Type == "s3" {
				cfg.Storage.Tiers[i].Region = region
			}
		}
	}
	if cmd.Flags().Changed("index-db") {
		path, _ := cmd.Flags().GetString("index-db")
		cfg.Cache.Index.Path = path
	}
	if cmd.Flags().Changed("no-index") {
		disabled, _ := cmd.Flags().GetBool("no-index")
		enabled := !disabled
		cfg.Cache.Index.Enabled = &enabled
	}

	return cfg, nil
}

// indexEnabled returns whether the index should be opened. Defaults to
// true when the config doesn't say otherwise.
func indexEnabled(cfg *config.Config) bool {
	if cfg.Cache.Index.Enabled == nil {
		return true
	}
	return *cfg.Cache.Index.Enabled
}

// indexPath resolves the SQLite DB path. Defaults to <local-dir>/index.db
// where <local-dir> is the first local storage tier's Dir.
func indexPath(cfg *config.Config) (string, error) {
	if cfg.Cache.Index.Path != "" {
		return cfg.Cache.Index.Path, nil
	}
	for _, t := range cfg.Storage.Tiers {
		if t.Type == "local" {
			dir := t.Dir
			if dir == "" {
				homeDir, _ := os.UserHomeDir()
				dir = filepath.Join(homeDir, ".escrow-proxy", "cache")
			}
			return filepath.Join(dir, "index.db"), nil
		}
	}
	return "", fmt.Errorf("index requires a local storage tier; configure --storage local or pass --index-db PATH")
}

// buildIndex opens the SQLite index (creating the DB and dirs as needed).
// Returns nil if the index is disabled via config or --no-index.
func buildIndex(cfg *config.Config) (*index.Index, error) {
	if !indexEnabled(cfg) {
		return nil, nil
	}
	path, err := indexPath(cfg)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating index dir: %w", err)
	}
	return index.Open(path, index.Options{
		FlushInterval:  cfg.Cache.Index.FlushInterval,
		FlushThreshold: cfg.Cache.Index.FlushThreshold,
	})
}

// buildLocalStorage returns a Storage backed by only the local tier of
// cfg.Storage.Tiers. Used by eviction so cloud tiers are never touched.
func buildLocalStorage(cfg *config.Config) (storage.Storage, error) {
	for _, t := range cfg.Storage.Tiers {
		if t.Type != "local" {
			continue
		}
		dir := t.Dir
		if dir == "" {
			homeDir, _ := os.UserHomeDir()
			dir = filepath.Join(homeDir, ".escrow-proxy", "cache")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating local storage dir: %w", err)
		}
		return storage.NewLocal(dir), nil
	}
	return nil, fmt.Errorf("no local storage tier configured")
}

// compileExcludes turns the configured regex strings into compiled patterns.
// Load() already validated them, so compilation should not fail.
func compileExcludes(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile exclude pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// buildStorage creates a storage.Storage from the config tiers.
func buildStorage(cfg *config.Config) (storage.Storage, error) {
	ctx := context.Background()
	var tiers []storage.Storage

	for _, t := range cfg.Storage.Tiers {
		switch t.Type {
		case "local":
			dir := t.Dir
			if dir == "" {
				homeDir, _ := os.UserHomeDir()
				dir = filepath.Join(homeDir, ".escrow-proxy", "cache")
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("creating local storage dir: %w", err)
			}
			tiers = append(tiers, storage.NewLocal(dir))
		case "gcs":
			s, err := storage.NewGCS(ctx, t.Bucket, t.Prefix)
			if err != nil {
				return nil, fmt.Errorf("creating GCS storage: %w", err)
			}
			tiers = append(tiers, s)
		case "s3":
			s, err := storage.NewS3(ctx, t.Bucket, t.Prefix, t.Region)
			if err != nil {
				return nil, fmt.Errorf("creating S3 storage: %w", err)
			}
			tiers = append(tiers, s)
		default:
			return nil, fmt.Errorf("unknown storage type: %s", t.Type)
		}
	}

	if len(tiers) == 0 {
		return nil, fmt.Errorf("no storage tiers configured")
	}
	if len(tiers) == 1 {
		return tiers[0], nil
	}
	return storage.NewTiered(tiers), nil
}

// setupLogger creates a structured logger at the given level.
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

// caDir returns the default directory for CA files.
func caDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".escrow-proxy")
}

// startProxy starts the HTTP server and handles graceful shutdown on signals.
func startProxy(handler http.Handler, listen string, logger *slog.Logger, onShutdown func()) {
	srv := &http.Server{
		Addr:    listen,
		Handler: handler,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		logger.Info("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	logger.Info("starting proxy", "listen", listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}

	if onShutdown != nil {
		onShutdown()
	}
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

			ca, err := tlspkg.EnsureCA(caDir(), cfg.CA.Cert, cfg.CA.Key)
			if err != nil {
				return fmt.Errorf("setting up CA: %w", err)
			}

			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}

			idx, err := buildIndex(cfg)
			if err != nil {
				return err
			}

			c := cache.New(store).WithIndex(idx)

			// Auto-reindex on first boot when storage has entries but the
			// index is empty. Run in the background so the proxy starts
			// serving requests immediately — Cache.Get reads from storage,
			// not the index, so an in-progress index is harmless.
			if idx != nil {
				if n, err := idx.Count(cmd.Context()); err == nil && n == 0 {
					empty, _ := storageHasNoEntries(cmd.Context(), c)
					if !empty {
						logger.Info("auto-reindex: index is empty but cache has entries; rebuilding in background")
						go func() {
							if ins, upd, rem, err := c.Reindex(cmd.Context()); err != nil {
								logger.Warn("auto-reindex failed; run `cache reindex`", "error", err)
							} else {
								logger.Info("auto-reindex complete", "inserted", ins, "updated", upd, "removed", rem)
							}
						}()
					}
				}
			}

			certCache := tlspkg.NewCertCache(ca, 1000)

			excludes, err := compileExcludes(cfg.Cache.ExcludePatterns)
			if err != nil {
				return err
			}

			handler := proxy.New(&proxy.Config{
				Mode:            proxy.ModeServe,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
				Methods:         cfg.Cache.Methods,
				ExcludePatterns: excludes,
				UpstreamTimeout: cfg.UpstreamTimeout,
				Logger:          logger,
			})

			startProxy(handler, cfg.Listen, logger, func() {
				if idx != nil {
					_ = idx.Close()
				}
			})
			return nil
		},
	}
}

// storageHasNoEntries reports whether the cache has no .meta entries.
// Used by serve startup to skip auto-reindex on a fresh deploy.
func storageHasNoEntries(ctx context.Context, c *cache.Cache) (bool, error) {
	empty := true
	err := c.Walk(ctx, func(string, *cache.EntryMeta) error {
		empty = false
		return errFirstEntryFound
	})
	if err != nil && !errors.Is(err, errFirstEntryFound) {
		return false, err
	}
	return empty, nil
}

var errFirstEntryFound = fmt.Errorf("found one")

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

			// Apply record-specific flags
			if cmd.Flags().Changed("output") {
				cfg.Record.Output, _ = cmd.Flags().GetString("output")
			}
			if cmd.Flags().Changed("format") {
				cfg.Record.Format, _ = cmd.Flags().GetString("format")
			}
			if cmd.Flags().Changed("oci-entries-per-layer") {
				cfg.Record.OCIEntriesPerLayer, _ = cmd.Flags().GetInt("oci-entries-per-layer")
			}

			if cfg.Record.Output == "" {
				return fmt.Errorf("--output is required for record mode")
			}

			ca, err := tlspkg.EnsureCA(caDir(), cfg.CA.Cert, cfg.CA.Key)
			if err != nil {
				return fmt.Errorf("setting up CA: %w", err)
			}

			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}

			// Determine archive format
			formatName := cfg.Record.Format
			if formatName == "" {
				formatName = archive.DetectFormat(cfg.Record.Output)
			}
			archFmt := archive.NewFormat(formatName, cfg.Record.OCIEntriesPerLayer)
			writer, err := archFmt.NewWriter(cfg.Record.Output)
			if err != nil {
				return fmt.Errorf("creating archive writer: %w", err)
			}

			baseCacheObj := cache.New(store)
			rec := cache.NewRecorder(baseCacheObj, writer)
			c := rec.Cache()
			certCache := tlspkg.NewCertCache(ca, 1000)

			excludes, err := compileExcludes(cfg.Cache.ExcludePatterns)
			if err != nil {
				return err
			}

			handler := proxy.New(&proxy.Config{
				Mode:            proxy.ModeRecord,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
				Methods:         cfg.Cache.Methods,
				ExcludePatterns: excludes,
				UpstreamTimeout: cfg.UpstreamTimeout,
				Logger:          logger,
			})

			startProxy(handler, cfg.Listen, logger, func() {
				logger.Info("finalizing archive", "output", cfg.Record.Output)
				if err := rec.Finalize(); err != nil {
					logger.Error("failed to finalize archive", "error", err)
				} else {
					logger.Info("archive finalized", "output", cfg.Record.Output)
				}
			})
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

			if cmd.Flags().Changed("archive") {
				cfg.Offline.Archive, _ = cmd.Flags().GetString("archive")
			}
			if cmd.Flags().Changed("allow-fallback") {
				cfg.Offline.AllowFallback, _ = cmd.Flags().GetBool("allow-fallback")
			}

			if cfg.Offline.Archive == "" {
				return fmt.Errorf("--archive is required for offline mode")
			}

			ca, err := tlspkg.EnsureCA(caDir(), cfg.CA.Cert, cfg.CA.Key)
			if err != nil {
				return fmt.Errorf("setting up CA: %w", err)
			}

			// Open archive
			formatName := archive.DetectFormat(cfg.Offline.Archive)
			archFmt := archive.NewFormat(formatName, 0)
			reader, err := archFmt.NewReader(cfg.Offline.Archive)
			if err != nil {
				return fmt.Errorf("opening archive: %w", err)
			}
			defer reader.Close()

			var store storage.Storage
			store = cache.NewArchiveStorage(reader)

			// If allow-fallback, layer real storage underneath
			if cfg.Offline.AllowFallback {
				realStore, err := buildStorage(cfg)
				if err != nil {
					logger.Warn("could not build fallback storage, using archive only", "error", err)
				} else {
					store = storage.NewTiered([]storage.Storage{store, realStore})
				}
			}

			c := cache.New(store)
			certCache := tlspkg.NewCertCache(ca, 1000)

			mode := proxy.ModeOffline
			if cfg.Offline.AllowFallback {
				mode = proxy.ModeServe
			}

			excludes, err := compileExcludes(cfg.Cache.ExcludePatterns)
			if err != nil {
				return err
			}

			handler := proxy.New(&proxy.Config{
				Mode:            mode,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
				Methods:         cfg.Cache.Methods,
				ExcludePatterns: excludes,
				UpstreamTimeout: cfg.UpstreamTimeout,
				Logger:          logger,
				AllowFallback:   cfg.Offline.AllowFallback,
			})

			startProxy(handler, cfg.Listen, logger, nil)
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
			certPath, _ := cmd.Flags().GetString("ca-cert")
			keyPath, _ := cmd.Flags().GetString("ca-key")

			ca, err := tlspkg.EnsureCA(caDir(), certPath, keyPath)
			if err != nil {
				return fmt.Errorf("loading CA: %w", err)
			}

			pemBytes := tlspkg.ExportCAPEM(ca)
			_, err = os.Stdout.Write(pemBytes)
			return err
		},
	}
	caCmd.AddCommand(exportCmd)
	return caCmd
}
