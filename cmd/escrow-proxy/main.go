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
	rootCmd.PersistentFlags().String("storage", "local", "comma-separated storage tier list (e.g., local,gcs)")
	rootCmd.PersistentFlags().String("local-dir", "", "local cache directory (default: ~/.escrow-proxy/cache/)")
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

// loadConfig loads the YAML config file (if any) and applies CLI flag overrides.
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	cfgPath, _ := cmd.Flags().GetString("config")
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

	return cfg, nil
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

			c := cache.New(store)
			certCache := tlspkg.NewCertCache(ca, 1000)

			handler := proxy.New(&proxy.Config{
				Mode:            proxy.ModeServe,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
				UpstreamTimeout: cfg.UpstreamTimeout,
				Logger:          logger,
			})

			startProxy(handler, cfg.Listen, logger, nil)
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

			handler := proxy.New(&proxy.Config{
				Mode:            proxy.ModeRecord,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
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

			handler := proxy.New(&proxy.Config{
				Mode:            mode,
				Cache:           c,
				CertCache:       certCache,
				CA:              ca,
				KeyHeaders:      cfg.Cache.KeyHeaders,
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
