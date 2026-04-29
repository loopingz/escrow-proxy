# Cache Inspection Commands

**Status:** approved
**Date:** 2026-04-28
**Owner:** Remi Cattiau

## Problem

`escrow-proxy cache invalidate` lets operators delete cache entries, but there
is no read-side counterpart. Operators currently cannot answer:

1. Is a given URL cached?
2. What is the digest (cache key) of a cached entry?
3. What does the cached response look like — status, headers, body?

These questions come up when debugging "why is the proxy returning stale data?",
when verifying a record-mode session, or when sanity-checking what an offline
archive will serve.

## Goals

- Add two read-only subcommands under the existing `cache` group:
  - `cache list` — find entries by URL/URL-prefix/method/key
  - `cache show` — inspect one entry's metadata, optionally dump body bytes
- Reuse the storage and cache layers already wired by `cache invalidate` —
  same flag vocabulary (`--url`, `--url-prefix`, `--method`, `--key`), same
  config plumbing, same cache walk.
- Add a `Size` capability to the storage interface so listings can show
  body size cheaply on every backend.

## Non-Goals

- Pagination beyond a hard `--limit` cap on unfiltered listings.
- Search by header value, status code, or body content. Filters stay
  URL-based, matching `invalidate`.
- Modifying entries. Both new commands are read-only.
- Backfilling size data for historical entries. The new `Size` is computed
  from storage at read time, not persisted in `EntryMeta`.

## CLI Surface

### `cache list`

```
escrow-proxy cache list [flags]

Flags:
  --url <string>          exact URL match
  --url-prefix <string>   URL prefix match
  --method <string>       narrow --url/--url-prefix to a specific HTTP method
  --key <string>          single entry by digest (skips the walk)
  --limit <int>           cap output rows (0 = unlimited; default 0)
  --json                  emit NDJSON instead of columns
```

**Filter rules** (mirror `invalidate` where they overlap):

- `--url`, `--url-prefix`, `--key` are mutually exclusive.
- `--method` is only valid with `--url` or `--url-prefix`.
- The `--limit` flag is `0` by default, meaning "no cap". When **no**
  filter is supplied **and** `--limit` is unset (still `0`), `list`
  applies an implicit cap of `1000` to protect against unbounded scans
  on large stores. When that implicit cap truncates, a stderr note is
  emitted: `truncated at 1000 entries; use a filter to narrow or pass --limit`.
- An explicit `--limit N` always wins (including `--limit 0` to opt out
  of the implicit cap on no-filter listings).

**Default text output** — one row per entry, space-separated columns in the
order: `<key>  <method>  <status>  <body-bytes>  <url>`. `<status>` is the
decimal HTTP status code from `EntryMeta.StatusCode`. `<body-bytes>` is
the raw integer from `cache.Size` (no unit suffix) so `awk`/`sort -n`
work. Zero matches prints nothing and exits 0.

**`--json` output** — one JSON object per line (NDJSON), e.g.:

```json
{"key":"abc123...","method":"GET","url":"https://example.com/x","status":200,"body_size":1234}
```

**Exit codes:**
- 0 — including zero matches
- non-zero — only on storage errors or invalid flag combinations

### `cache show`

```
escrow-proxy cache show [flags]

Flags:
  --key <string>          exact digest (mutually exclusive with --url)
  --url <string>          locate by URL; errors if >1 entry matches
  --method <string>       narrow --url to a specific HTTP method
  --json                  structured output instead of header block
  --body                  also emit body bytes
  --output <path>         write body to a file (implies --body)
```

**Lookup rules:**

- Exactly one of `--key` or `--url` must be supplied.
- `--method` is only valid with `--url`.
- `--key` not found → exit non-zero, `not found` to stderr.
- `--url` zero matches → exit non-zero, `not found`.
- `--url` multiple matches → exit non-zero. Stderr lists each candidate as
  `<key>  <method>  <url>` and tells the user to pass `--key`. Example:

  ```
  multiple entries match URL; pass --key to disambiguate:
    abc123...  GET   https://example.com/x
    def456...  POST  https://example.com/x
  ```

**Default text output** — header block written to stdout:

```
key:        <hex>
method:     GET
url:        https://example.com/x
status:     200
body-size:  1234
headers:
  Content-Type: application/json
  Cache-Control: no-store
```

Header names are sorted alphabetically; multiple values for the same
header repeat the line.

**`--json` output** — single JSON object on stdout:

```json
{
  "key": "...",
  "method": "GET",
  "url": "...",
  "status": 200,
  "body_size": 1234,
  "headers": {"Content-Type": ["application/json"]}
}
```

**`--body` semantics:**

- `--output PATH` — stream the body to that file (truncate/create, mode
  0644). On success, print `wrote <N> bytes to <PATH>` to stderr. The
  metadata block (or JSON) still goes to stdout as usual.
- `--body` alone — stream the body to stdout, but **only if stdout is not
  a TTY**. If stdout is a TTY, exit non-zero with stderr message:
  `refusing to print body to terminal; use --output PATH or pipe to a file`.
- `--output` implies `--body` — passing `--output` without `--body` is
  treated as if `--body` were set, not an error.
- When `--body` is combined with `--json`, the JSON metadata still goes
  to stdout; the body still goes to `--output` (file) or to stdout
  appended after the JSON line. Mixing `--body` (no `--output`) with
  `--json` and a non-TTY stdout therefore writes one JSON line followed
  by raw bytes — callers wanting both should prefer `--output` so the
  two streams stay separated.

## Architecture

### Storage interface change

Extend `internal/storage/storage.go`:

```go
type Storage interface {
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Put(ctx context.Context, key string, r io.Reader) error
    Exists(ctx context.Context, key string) (bool, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]string, error)
    Size(ctx context.Context, key string) (int64, error)  // new
}
```

`Size` returns `storage.ErrNotFound` when the key is absent — same
contract as `Get`/`Delete`.

Implementations:

- `local` — `os.Stat(filepath).Size()`; wrap `os.IsNotExist` as `ErrNotFound`.
- `gcs` — `obj.Attrs(ctx).Size`; map `storage.ErrObjectNotExist` to `ErrNotFound`.
- `s3` — `HeadObject` → `*resp.ContentLength`; map `NotFound`/`NoSuchKey` to `ErrNotFound`.
- `tiered` — iterate tiers; on `ErrNotFound` move to next, otherwise return
  the size or the error. (Same shape as the existing `Get` cascade.)
- `cache.ArchiveStorage` — body bytes are already in memory after `Get`;
  return `len(body)`. If the archive layer doesn't expose size without
  reading, fall back to a Get-and-count for that backend only.

### Cache helper

Add to `internal/cache/cache.go`:

```go
func (c *Cache) Size(ctx context.Context, key string) (int64, error) {
    return c.storage.Size(ctx, bodyKey(key))
}
```

Body, not meta — the public size is what an operator cares about, and the
meta blob is an implementation detail.

### Command structure

Everything lives in `cmd/escrow-proxy/cache.go` (and its existing test
file). Two new option structs and run functions modeled on
`invalidateOptions` / `runInvalidate`:

```go
type listOptions struct {
    Cache      *cache.Cache
    Key        string
    URL        string
    URLPrefix  string
    Method     string
    Limit      int
    JSON       bool
    Stdout     io.Writer
    Stderr     io.Writer
}
func runList(ctx context.Context, opts listOptions) error

type showOptions struct {
    Cache      *cache.Cache
    Key        string
    URL        string
    Method     string
    JSON       bool
    Body       bool
    Output     string  // empty = stdout
    StdoutIsTTY bool
    Stdout     io.Writer
    Stderr     io.Writer
}
func runShow(ctx context.Context, opts showOptions) error
```

The TTY check happens in the Cobra wrapper (`isatty(os.Stdout.Fd())`) and
is passed down as a bool so tests can simulate either case without real
file descriptors.

A shared helper holds the URL/prefix/method predicate so `runInvalidate`,
`runList`, and `runShow` all use the same matching logic. Suggested name:

```go
func newURLPredicate(url, urlPrefix, method string) func(*cache.EntryMeta) bool
```

Cobra wiring adds `newCacheListCmd()` and `newCacheShowCmd()`, both
registered under `newCacheCmd()` alongside the existing `invalidate`.

## Data Flow

### `list`

1. Build storage + cache from config (same path as `invalidate`).
2. If `--key` is set, call `cache.Get` directly to fetch one entry's meta,
   then `cache.Size` for body size; print one row.
3. Otherwise, call `cache.Walk`. For each entry:
   - Apply the URL/prefix/method predicate (no-op when no filter).
   - Call `cache.Size` for the body bytes.
   - Render one row (text or JSON).
   - Stop after `--limit` rows; emit truncation note if applicable.

### `show`

1. Build storage + cache from config.
2. Resolve the target key:
   - `--key` → use as-is.
   - `--url` → walk with the URL/method predicate, collect candidates.
     Zero → not-found error. Two+ → ambiguity error with candidate list.
3. `cache.Get` returns `(meta, bodyRC)`.
4. If neither `--body` nor `--output` is set, close `bodyRC` immediately
   and print only the metadata (text or JSON).
5. If `--output` is set, stream `bodyRC` to the file.
6. Else if `--body` is set:
   - TTY → close `bodyRC`, error.
   - Non-TTY → stream `bodyRC` to stdout (after the metadata block / JSON
     line).

## Error Handling

- Flag validation errors (mutual exclusion, `--method` without
  `--url`/`--url-prefix`, missing required arg) → return error from
  Cobra `RunE`, exit code 1, stderr message.
- Storage `ErrNotFound` from a single-key lookup → exit code 1, `not found`.
- Other storage errors → wrapped and bubbled up.
- Per-row failures in `list` (a corrupt entry's `Size` errors) — log to
  stderr, render `?` in the size column, continue. Match `cache.Walk`'s
  existing tolerance for unreadable entries.
- TTY refusal in `show --body` — exit code 1, no body written.

## Testing

Match the existing pattern in `cmd/escrow-proxy/cache_test.go`:

- Reuse `newTestCache` and the `put(key, method, url)` helper.
- Drive `runList` and `runShow` through option structs; capture
  `stdout`/`stderr`/`error`.

**`runList` cases:**

- Zero filter, fewer than 1000 entries → all rows, no truncation note.
- Zero filter, 1001 entries → 1000 rows + stderr truncation note.
- `--url` exact match (multiple methods) → 2 rows.
- `--url-prefix` match.
- `--url-prefix` + `--method` narrows.
- `--key` returns one row.
- `--key` not found → not-found error.
- `--json` flag emits NDJSON; each line is parseable JSON with the
  documented fields.
- `--method` without `--url` / `--url-prefix` → validation error.
- Mutual exclusion errors.

**`runShow` cases:**

- `--key` happy path → metadata block contains all expected fields.
- `--key` not found → not-found error.
- `--url` single match.
- `--url` multi-match → ambiguity error lists candidates.
- `--url` zero match → not-found error.
- `--json` emits one parseable JSON object with expected fields.
- `--body` + `--output` → file contains the seeded body bytes; stderr
  has `wrote N bytes to PATH`.
- `--body` with `StdoutIsTTY=true` → error, body not written.
- `--body` with `StdoutIsTTY=false` → bytes appended to stdout after
  metadata.
- `--output` without `--body` → behaves as if `--body` were set.

**Storage layer:**

- `local`: `Size` returns the file size; `ErrNotFound` for missing keys.
  Use `t.TempDir()`.
- `tiered`: `Size` falls through on `ErrNotFound` and returns from the
  first tier that has the key. Existing in-memory test fixtures already
  cover this pattern for `Get`.
- `gcs` / `s3` tests follow the existing emulator patterns in those
  packages — round-trip a put, then assert `Size` matches the put length.

## File Touches

- `internal/storage/storage.go` — add `Size` to interface.
- `internal/storage/local.go` + `local_test.go`.
- `internal/storage/gcs.go` + `gcs_test.go`.
- `internal/storage/s3.go` + `s3_test.go`.
- `internal/storage/tiered.go` + `tiered_test.go`.
- `internal/cache/archive_storage.go` + `cache_test.go` (if `Size` lives
  on the archive-backed storage).
- `internal/cache/cache.go` — `Cache.Size` helper.
- `cmd/escrow-proxy/cache.go` — `runList`, `runShow`, shared predicate
  helper, two new Cobra commands.
- `cmd/escrow-proxy/cache_test.go` — list/show tests.
- `README.md` — usage examples for `cache list` and `cache show`.
- `docs/configuration.md` — mention the new commands in the cache
  management section if one exists.

## Open Questions

None. Ready for implementation planning.
