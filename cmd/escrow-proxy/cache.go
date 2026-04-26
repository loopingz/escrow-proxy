package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/storage"
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

	targets, err := collectTargets(ctx, opts)
	if err != nil {
		return err
	}
	return executeDeletes(ctx, opts, targets)
}

type invalidateTarget struct {
	Key    string
	Method string
	URL    string
}

func collectTargets(ctx context.Context, opts invalidateOptions) ([]invalidateTarget, error) {
	switch {
	case opts.Key != "":
		meta, body, err := opts.Cache.Get(ctx, opts.Key)
		if err != nil {
			return nil, fmt.Errorf("locating entry: %w", err)
		}
		body.Close()
		return []invalidateTarget{{Key: opts.Key, Method: meta.Method, URL: meta.URL}}, nil
	case opts.URL != "":
		return scanTargets(ctx, opts.Cache, func(meta *cache.EntryMeta) bool {
			if opts.Method != "" && !strings.EqualFold(meta.Method, opts.Method) {
				return false
			}
			return meta.URL == opts.URL
		})
	case opts.URLPrefix != "":
		return scanTargets(ctx, opts.Cache, func(meta *cache.EntryMeta) bool {
			if opts.Method != "" && !strings.EqualFold(meta.Method, opts.Method) {
				return false
			}
			return strings.HasPrefix(meta.URL, opts.URLPrefix)
		})
	}
	return nil, errors.New("not implemented") // filled in by later tasks
}

func scanTargets(ctx context.Context, c *cache.Cache, match func(*cache.EntryMeta) bool) ([]invalidateTarget, error) {
	var out []invalidateTarget
	err := c.Walk(ctx, func(key string, meta *cache.EntryMeta) error {
		if match(meta) {
			out = append(out, invalidateTarget{Key: key, Method: meta.Method, URL: meta.URL})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning cache: %w", err)
	}
	return out, nil
}

func executeDeletes(ctx context.Context, opts invalidateOptions, targets []invalidateTarget) error {
	verb := "deleted"
	if opts.DryRun {
		verb = "would delete"
	}

	failed := 0
	raced := 0
	for _, tg := range targets {
		if !opts.DryRun {
			if err := opts.Cache.Delete(ctx, tg.Key); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					raced++
					continue // race with concurrent delete; not a failure
				}
				fmt.Fprintf(opts.Stderr, "failed to delete %s: %v\n", tg.Key, err)
				failed++
				continue
			}
		}
		fmt.Fprintf(opts.Stdout, "%s %s %s\n", tg.Key, tg.Method, tg.URL)
	}

	fmt.Fprintf(opts.Stderr, "%s %d entries\n", verb, len(targets)-failed-raced)
	if failed > 0 {
		return fmt.Errorf("%d entries failed to delete", failed)
	}
	return nil
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
