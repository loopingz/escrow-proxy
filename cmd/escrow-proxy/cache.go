package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/loopingz/escrow-proxy/internal/cache"
)

// invalidateOptions carries everything runInvalidate needs.
// The CLI layer constructs this; tests construct it directly.
type invalidateOptions struct {
	Cache *cache.Cache

	Key       string
	URL       string
	URLPrefix string
	All       bool
	Method    string
	DryRun    bool

	Stdout io.Writer
	Stderr io.Writer
}

// runInvalidate executes the cache invalidate command.
// Returns nil on success, an error otherwise. Per-entry failures during
// bulk operations are logged to Stderr and counted; the function returns
// an error if any entry failed to delete.
func runInvalidate(ctx context.Context, opts invalidateOptions) error {
	if err := validateInvalidateFilters(opts); err != nil {
		return err
	}
	return errors.New("not implemented")
}

func validateInvalidateFilters(opts invalidateOptions) error {
	count := 0
	if opts.Key != "" {
		count++
	}
	if opts.URL != "" {
		count++
	}
	if opts.URLPrefix != "" {
		count++
	}
	if opts.All {
		count++
	}
	if count == 0 {
		return fmt.Errorf("exactly one of --key, --url, --url-prefix, --all must be specified")
	}
	if count > 1 {
		return fmt.Errorf("flags --key, --url, --url-prefix, --all are mutually exclusive")
	}
	if opts.All && opts.Method != "" {
		return fmt.Errorf("--method cannot be combined with --all")
	}
	return nil
}
