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
