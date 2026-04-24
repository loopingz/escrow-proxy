# Cache Invalidate CLI — Design

**Date:** 2026-04-24
**Status:** approved

## Problem

The proxy caches responses indefinitely in tiered storage (local + optional GCS/S3). When a cached entry becomes wrong — upstream shipped a bad artifact, a transient 200 got cached that shouldn't have been, a test run polluted the cache — there is no way to remove it short of deleting storage directories by hand and hoping key layout does not change.

We need a first-class way to reset cache entries: one key, one URL, a URL prefix, or everything, hitting every configured storage tier.

## Out of scope

- HTTP admin endpoint on the running proxy. Chosen against in brainstorming — CLI-direct-to-storage is simpler and works without adding an admin port.
- Narrowing `--url` matches by request headers (`--header "Accept: ..."`). Blocked by `EntryMeta` not storing request headers used in the cache key; flagged as a follow-up, not part of this spec.
- Authentication / ACLs on invalidation. The CLI runs with the operator's storage credentials; that's the access boundary.

## Command surface

New subcommand group `cache`, with `invalidate` as the first member (leaves room for `cache list`, `cache stat`, etc.).

```
escrow-proxy cache invalidate [filter] [--method M] [--dry-run]
```

### Filters (exactly one required)

| Flag | Semantics |
|------|-----------|
| `--key <hex>` | Delete the single cache entry with this SHA256 key. No scan. |
| `--url <url>` | Scan all entries; delete those whose stored `EntryMeta.URL` equals this string. |
| `--url-prefix <prefix>` | Scan all entries; delete those whose `EntryMeta.URL` begins with this prefix. |
| `--all` | Delete every `.meta`/`.body` pair. |

Passing zero or more than one filter is a usage error (exit non-zero before touching storage).

### Modifiers

| Flag | Semantics |
|------|-----------|
| `--method <GET\|POST\|...>` | Optional narrower. Combines with `--url` or `--url-prefix`. Ignored with `--key` (key already encodes method) and rejected with `--all`. |
| `--dry-run` | Print matches; do not delete. |

### Inherited flags

`--config`, `--storage`, `--local-dir`, `--gcs-bucket`, `--gcs-prefix`, `--s3-bucket`, `--s3-prefix`, `--s3-region` — same as `serve`/`record`. The CLI resolves storage tiers identically to the running proxy, so an operator invoking `cache invalidate` with the same config targets the same cache.

### Output

- **stdout:** one line per affected entry: `<key> <method> <url>`. This is machine-parseable; scripts can pipe it.
- **stderr:** a single summary line at end: `deleted N entries` (or `would delete N entries` on `--dry-run`).
- **Exit codes:**
  - `0` — success, including zero-match scans.
  - non-zero — usage error, storage failure, or `--key` pointing at a missing entry.

## Architecture

### Storage layer — no changes required

`internal/storage/tiered.go` already handles what we need:

- `Tiered.Delete` (tiered.go:79) fans out to every tier concurrently and fails if any tier errors. **One refinement:** treat per-tier `ErrNotFound` as success inside this fan-out, so a delete of an entry that exists in L1 but not yet in L2 doesn't fail the whole operation. Callers that care about existence use `Exists` first.
- `Tiered.List` (tiered.go:99) already unions and deduplicates keys across tiers.
- Per-tier `Delete` (local, GCS, S3) is already implemented by the `Storage` interface.

### Cache layer — two new methods

`internal/cache/cache.go`:

```go
// Delete removes both <key>.meta and <key>.body.
// Returns ErrNotFound (wrapped) if meta is absent.
// If meta delete succeeds but body delete fails, logs and returns nil —
// the entry is already unreachable via Exists() which checks meta.
func (c *Cache) Delete(ctx context.Context, key string) error

// Walk iterates every cached entry and calls fn with the parsed meta.
// Returning an error from fn stops the walk.
func (c *Cache) Walk(ctx context.Context, fn func(key string, meta *EntryMeta) error) error
```

`Walk` is backed by `storage.List(".meta")`, stripping the `.meta` suffix to recover the key, then fetching and unmarshaling the meta blob for each. The scan is O(n) in cache size; this is the price of content-addressed storage with opaque keys.

A `.meta` blob that fails to fetch or unmarshal is logged and skipped (not fatal) — one corrupt entry must not block invalidation of the rest.

### CLI layer — new files

```
cmd/escrow-proxy/
  cache.go         # newCacheCmd(), newCacheInvalidateCmd()
  cache_test.go    # end-to-end tests against a local-only storage tier
```

`main.go` registers the new group:

```go
rootCmd.AddCommand(newCacheCmd())
```

### Control flow

```
parseFlags
validateExactlyOneFilter  // usage error if violated
loadConfig                 // reuses loadConfig() from main.go
buildStorage               // reuses buildStorage() from main.go
c := cache.New(store)

switch filter {
case --key:
    targets = [{key, meta?}]       // optional Get for output; fall back to bare key
case --url, --url-prefix, --all:
    c.Walk { collect entries matching predicate (+optional --method) }
}

if dry-run:
    print each target
    summary("would delete N entries")
    return 0

for each target:
    c.Delete(ctx, target.key)
    on success: print target
    on ErrNotFound: skip silently   // race with concurrent delete; scan said it existed
    on other error: log, remember to exit non-zero
summary("deleted N entries")
return (0 if all ok, non-zero if any non-ErrNotFound failure)
```

## Concurrency with a running proxy

Safe. All backends tolerate concurrent reads/deletes. The only meaningful race is `Put(key)` from the proxy landing between `List` and `Delete` of a scan — the new entry survives, which is the correct behavior (it reflects post-invalidation state).

## Testing

### `internal/cache/cache_test.go` — additions

- `Delete` removes both meta and body for an existing key.
- `Delete` of a missing key returns an error wrapping `storage.ErrNotFound`.
- `Delete` when meta is absent but body is present returns an error wrapping `ErrNotFound` (meta is the source of truth for existence).
- `Delete` when meta deletes but body delete fails returns nil (best-effort semantics).
- `Walk` visits every `Put` entry exactly once with the correct key and meta.
- `Walk` on an empty cache returns nil without calling fn.
- `Walk` propagates an error returned by fn and stops iteration.

### `internal/storage/tiered_test.go` — addition

One test for the `ErrNotFound` refinement in `Tiered.Delete`: deleting a key that exists in one tier but not another returns nil, and the key is gone from the tier that had it.

### `cmd/escrow-proxy/cache_test.go` — new file

Table-driven tests backed by `t.TempDir()` local storage. Each test seeds the cache with a known set of entries via `cache.Put`, invokes the CLI command with specific flags, and asserts the final state:

- `--key <hex>` deletes exactly that entry, leaves others intact.
- `--key <missing>` exits non-zero, no state change.
- `--url <u>` deletes every variation of that URL (e.g. GET and POST both removed).
- `--url <u> --method GET` deletes only the GET entry, leaves the POST.
- `--url-prefix https://foo/` deletes matching entries, leaves non-matching.
- `--all` deletes everything.
- `--dry-run --all` prints N lines, leaves storage intact.
- Zero filters → exit non-zero, no state change, usage message on stderr.
- Multiple filters → exit non-zero, no state change.
- `--all --method GET` → exit non-zero (method modifier invalid with `--all`).

Stdout content is parsed and asserted (count of lines, presence of expected keys). stderr summary line is asserted.

### Not covered

GCS/S3 backends — existing per-backend tests cover `Delete` and `List`. The invalidate command is tier-agnostic; a local tier in tests exercises the full command surface.

## Documentation updates

- `README.md` — add `cache invalidate` to the CLI reference section alongside `serve`, `record`, `offline`, `ca`.
- `docs/configuration.md` — no changes (no new config keys).
