package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/storage"
	"github.com/spf13/cobra"
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
	case opts.URL != "" || opts.URLPrefix != "":
		return scanTargets(ctx, opts.Cache, urlMethodPredicate(opts.URL, opts.URLPrefix, opts.Method))
	case opts.All:
		return scanTargets(ctx, opts.Cache, func(*cache.EntryMeta) bool { return true })
	}
	// unreachable: validateInvalidateFilters guarantees exactly one filter is set.
	return nil, errors.New("no filter matched")
}

// urlMethodPredicate returns a match function over EntryMeta. Exactly one of
// url or urlPrefix should be non-empty; method narrows either when set.
func urlMethodPredicate(url, urlPrefix, method string) func(*cache.EntryMeta) bool {
	return func(meta *cache.EntryMeta) bool {
		if method != "" && !strings.EqualFold(meta.Method, method) {
			return false
		}
		if url != "" {
			return meta.URL == url
		}
		if urlPrefix != "" {
			return strings.HasPrefix(meta.URL, urlPrefix)
		}
		return true
	}
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

// ---- list ----

const defaultListLimit = 1000

type listOptions struct {
	Cache *cache.Cache

	Key       string
	URL       string
	URLPrefix string
	Method    string
	Limit     int
	LimitSet  bool
	JSON      bool

	Stdout io.Writer
	Stderr io.Writer
}

type listEntry struct {
	Key      string      `json:"key"`
	Method   string      `json:"method"`
	URL      string      `json:"url"`
	Status   int         `json:"status"`
	BodySize int64       `json:"body_size"`
	Headers  http.Header `json:"headers,omitempty"`
}

func runList(ctx context.Context, opts listOptions) error {
	if err := validateListFilters(opts); err != nil {
		return err
	}

	hasFilter := opts.Key != "" || opts.URL != "" || opts.URLPrefix != ""
	limit := opts.Limit
	if !opts.LimitSet && !hasFilter {
		limit = defaultListLimit
	}

	entries, truncated, err := collectListEntries(ctx, opts, limit)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if opts.JSON {
			b, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("marshaling entry: %w", err)
			}
			fmt.Fprintln(opts.Stdout, string(b))
		} else {
			fmt.Fprintf(opts.Stdout, "%s %s %d %d %s\n", e.Key, e.Method, e.Status, e.BodySize, e.URL)
		}
	}

	if truncated {
		fmt.Fprintf(opts.Stderr, "truncated at %d entries; use a filter to narrow or pass --limit\n", limit)
	}
	return nil
}

func collectListEntries(ctx context.Context, opts listOptions, limit int) ([]listEntry, bool, error) {
	if opts.Key != "" {
		meta, body, err := opts.Cache.Get(ctx, opts.Key)
		if err != nil {
			return nil, false, fmt.Errorf("locating entry: %w", err)
		}
		body.Close()
		size, err := opts.Cache.Size(ctx, opts.Key)
		if err != nil {
			return nil, false, fmt.Errorf("sizing entry: %w", err)
		}
		return []listEntry{{Key: opts.Key, Method: meta.Method, URL: meta.URL, Status: meta.StatusCode, BodySize: size}}, false, nil
	}

	predicate := urlMethodPredicate(opts.URL, opts.URLPrefix, opts.Method)

	var out []listEntry
	truncated := false
	walkErr := opts.Cache.Walk(ctx, func(key string, meta *cache.EntryMeta) error {
		if !predicate(meta) {
			return nil
		}
		if limit > 0 && len(out) >= limit {
			truncated = true
			return errStopWalk
		}
		size, err := opts.Cache.Size(ctx, key)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "size %s: %v\n", key, err)
			size = -1
		}
		out = append(out, listEntry{
			Key:      key,
			Method:   meta.Method,
			URL:      meta.URL,
			Status:   meta.StatusCode,
			BodySize: size,
		})
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStopWalk) {
		return nil, false, fmt.Errorf("scanning cache: %w", walkErr)
	}
	return out, truncated, nil
}

var errStopWalk = errors.New("stop walk")

func validateListFilters(opts listOptions) error {
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
	if count > 1 {
		return fmt.Errorf("flags --key, --url, --url-prefix are mutually exclusive")
	}
	if opts.Method != "" && opts.URL == "" && opts.URLPrefix == "" {
		return fmt.Errorf("--method requires --url or --url-prefix")
	}
	if opts.LimitSet && opts.Limit < 0 {
		return fmt.Errorf("--limit must be >= 0")
	}
	return nil
}

// ---- show ----

type showOptions struct {
	Cache *cache.Cache

	Key    string
	URL    string
	Method string

	JSON        bool
	Body        bool
	Output      string
	StdoutIsTTY bool

	Stdout io.Writer
	Stderr io.Writer
}

type showEntry struct {
	Key      string      `json:"key"`
	Method   string      `json:"method"`
	URL      string      `json:"url"`
	Status   int         `json:"status"`
	BodySize int64       `json:"body_size"`
	Headers  http.Header `json:"headers"`
}

func runShow(ctx context.Context, opts showOptions) error {
	if err := validateShowFilters(opts); err != nil {
		return err
	}

	key, err := resolveShowKey(ctx, opts)
	if err != nil {
		return err
	}

	meta, bodyRC, err := opts.Cache.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("loading entry: %w", err)
	}
	defer bodyRC.Close()

	size, err := opts.Cache.Size(ctx, key)
	if err != nil {
		return fmt.Errorf("sizing entry: %w", err)
	}

	if err := writeShowMeta(opts, key, meta, size); err != nil {
		return err
	}

	wantBody := opts.Body || opts.Output != ""
	if !wantBody {
		return nil
	}

	if opts.Output != "" {
		f, err := os.OpenFile(opts.Output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("opening output: %w", err)
		}
		defer f.Close()
		n, err := io.Copy(f, bodyRC)
		if err != nil {
			return fmt.Errorf("writing body: %w", err)
		}
		fmt.Fprintf(opts.Stderr, "wrote %d bytes to %s\n", n, opts.Output)
		return nil
	}

	if opts.StdoutIsTTY {
		return fmt.Errorf("refusing to print body to terminal; use --output PATH or pipe to a file")
	}
	if _, err := io.Copy(opts.Stdout, bodyRC); err != nil {
		return fmt.Errorf("writing body: %w", err)
	}
	return nil
}

func writeShowMeta(opts showOptions, key string, meta *cache.EntryMeta, size int64) error {
	if opts.JSON {
		entry := showEntry{
			Key:      key,
			Method:   meta.Method,
			URL:      meta.URL,
			Status:   meta.StatusCode,
			BodySize: size,
			Headers:  meta.Header,
		}
		b, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshaling entry: %w", err)
		}
		fmt.Fprintln(opts.Stdout, string(b))
		return nil
	}

	fmt.Fprintf(opts.Stdout, "key:        %s\n", key)
	fmt.Fprintf(opts.Stdout, "method:     %s\n", meta.Method)
	fmt.Fprintf(opts.Stdout, "url:        %s\n", meta.URL)
	fmt.Fprintf(opts.Stdout, "status:     %d\n", meta.StatusCode)
	fmt.Fprintf(opts.Stdout, "body-size:  %d\n", size)
	fmt.Fprintln(opts.Stdout, "headers:")
	names := make([]string, 0, len(meta.Header))
	for n := range meta.Header {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, v := range meta.Header[n] {
			fmt.Fprintf(opts.Stdout, "  %s: %s\n", n, v)
		}
	}
	return nil
}

func resolveShowKey(ctx context.Context, opts showOptions) (string, error) {
	if opts.Key != "" {
		exists, err := opts.Cache.Exists(ctx, opts.Key)
		if err != nil {
			return "", fmt.Errorf("checking entry: %w", err)
		}
		if !exists {
			return "", fmt.Errorf("not found: %s", opts.Key)
		}
		return opts.Key, nil
	}

	predicate := urlMethodPredicate(opts.URL, "", opts.Method)
	type candidate struct {
		key, method, url string
	}
	var matches []candidate
	walkErr := opts.Cache.Walk(ctx, func(key string, meta *cache.EntryMeta) error {
		if predicate(meta) {
			matches = append(matches, candidate{key, meta.Method, meta.URL})
		}
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("scanning cache: %w", walkErr)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("not found: %s", opts.URL)
	}
	if len(matches) > 1 {
		fmt.Fprintln(opts.Stderr, "multiple entries match URL; pass --key to disambiguate:")
		for _, m := range matches {
			fmt.Fprintf(opts.Stderr, "  %s  %s  %s\n", m.key, m.method, m.url)
		}
		return "", fmt.Errorf("multiple entries match URL")
	}
	return matches[0].key, nil
}

func validateShowFilters(opts showOptions) error {
	if opts.Key == "" && opts.URL == "" {
		return fmt.Errorf("exactly one of --key or --url must be specified")
	}
	if opts.Key != "" && opts.URL != "" {
		return fmt.Errorf("--key and --url are mutually exclusive")
	}
	if opts.Method != "" && opts.URL == "" {
		return fmt.Errorf("--method requires --url")
	}
	return nil
}

// ---- Cobra wiring ----

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Cache management commands",
	}
	cmd.AddCommand(newCacheInvalidateCmd())
	cmd.AddCommand(newCacheListCmd())
	cmd.AddCommand(newCacheShowCmd())
	return cmd
}

func newCacheListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cache entries by URL, URL prefix, or key",
		Long: `List cache entries from the configured storage tiers.

Output columns: <key> <method> <status> <body-bytes> <url>.
Without any filter, listing is capped at 1000 entries by default;
pass --limit 0 to opt out of the cap, or use --url/--url-prefix/--key
to narrow.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}
			c := cache.New(store)

			key, _ := cmd.Flags().GetString("key")
			url, _ := cmd.Flags().GetString("url")
			urlPrefix, _ := cmd.Flags().GetString("url-prefix")
			method, _ := cmd.Flags().GetString("method")
			limit, _ := cmd.Flags().GetInt("limit")
			jsonOut, _ := cmd.Flags().GetBool("json")

			return runList(cmd.Context(), listOptions{
				Cache:     c,
				Key:       key,
				URL:       url,
				URLPrefix: urlPrefix,
				Method:    method,
				Limit:     limit,
				LimitSet:  cmd.Flags().Changed("limit"),
				JSON:      jsonOut,
				Stdout:    cmd.OutOrStdout(),
				Stderr:    cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().String("key", "", "exact cache key (hex SHA256)")
	cmd.Flags().String("url", "", "exact request URL")
	cmd.Flags().String("url-prefix", "", "request URL prefix")
	cmd.Flags().String("method", "", "narrow --url/--url-prefix to a specific HTTP method")
	cmd.Flags().Int("limit", 0, "cap output rows (0 = unlimited; implicit 1000 when no filter)")
	cmd.Flags().Bool("json", false, "emit one JSON object per line")
	return cmd
}

func newCacheShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show a single cache entry's metadata, optionally with body",
		Long: `Show a single cache entry. Pass --key for exact lookup, or --url
(optionally narrowed with --method) to locate by request URL.

Pass --body to also emit body bytes (refused if stdout is a terminal),
or --output PATH to write the body to a file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}
			c := cache.New(store)

			key, _ := cmd.Flags().GetString("key")
			url, _ := cmd.Flags().GetString("url")
			method, _ := cmd.Flags().GetString("method")
			jsonOut, _ := cmd.Flags().GetBool("json")
			body, _ := cmd.Flags().GetBool("body")
			output, _ := cmd.Flags().GetString("output")

			return runShow(cmd.Context(), showOptions{
				Cache:       c,
				Key:         key,
				URL:         url,
				Method:      method,
				JSON:        jsonOut,
				Body:        body,
				Output:      output,
				StdoutIsTTY: stdoutIsTTY(),
				Stdout:      cmd.OutOrStdout(),
				Stderr:      cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().String("key", "", "exact cache key (hex SHA256)")
	cmd.Flags().String("url", "", "request URL; errors if multiple entries match")
	cmd.Flags().String("method", "", "narrow --url to a specific HTTP method")
	cmd.Flags().Bool("json", false, "emit metadata as JSON")
	cmd.Flags().Bool("body", false, "also emit body bytes (refused if stdout is a TTY)")
	cmd.Flags().String("output", "", "write body to file (implies --body)")
	return cmd
}

// stdoutIsTTY reports whether os.Stdout refers to a character device. Used to
// guard --body from dumping binary into an interactive terminal.
func stdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func newCacheInvalidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invalidate",
		Short: "Delete cache entries by key, URL, URL prefix, or all",
		Long: `Delete cache entries from every configured storage tier.

Exactly one of --key, --url, --url-prefix, --all must be supplied.
--method narrows --url and --url-prefix to a specific HTTP method.
--dry-run reports what would be deleted without touching storage.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			store, err := buildStorage(cfg)
			if err != nil {
				return err
			}
			c := cache.New(store)

			key, _ := cmd.Flags().GetString("key")
			url, _ := cmd.Flags().GetString("url")
			urlPrefix, _ := cmd.Flags().GetString("url-prefix")
			all, _ := cmd.Flags().GetBool("all")
			method, _ := cmd.Flags().GetString("method")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			return runInvalidate(cmd.Context(), invalidateOptions{
				Cache:     c,
				Key:       key,
				URL:       url,
				URLPrefix: urlPrefix,
				All:       all,
				Method:    method,
				DryRun:    dryRun,
				Stdout:    cmd.OutOrStdout(),
				Stderr:    cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().String("key", "", "exact cache key (hex SHA256)")
	cmd.Flags().String("url", "", "exact request URL")
	cmd.Flags().String("url-prefix", "", "request URL prefix")
	cmd.Flags().Bool("all", false, "delete every entry")
	cmd.Flags().String("method", "", "narrow --url/--url-prefix to a specific HTTP method")
	cmd.Flags().Bool("dry-run", false, "print what would be deleted without deleting")
	return cmd
}
