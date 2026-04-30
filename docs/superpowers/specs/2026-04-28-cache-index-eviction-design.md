# Local SQLite Cache Index + Eviction

**Status:** approved (decisions made via brainstorm)
**Date:** 2026-04-28
**Owner:** Remi Cattiau

## Problem

Caches in production reach ~200 GB. The current model has no usage data
and no fast query path:

- No `last_accessed_at` or `hit_count` per entry.
- `cache list` / `cache show` walk every `.meta` file — slow at this scale.
- Eviction would also need to walk the whole tree to find LRU candidates.
- Operators cannot answer "shrink the cache to 150 GB by dropping the
  least-recently-used entries" without external tooling.

## Goals

- Persist per-entry usage stats (`created_at`, `last_accessed_at`,
  `hit_count`, `body_size`) plus query-friendly columns (`method`, `url`,
  `status`) in a local SQLite database alongside the cache directory.
- Add `cache evict` to shrink the local cache to a target size based on
  LRU order, with a safety knob (`--min-age`).
- Add `cache reindex` to rebuild the SQLite index from disk after manual
  changes, drift, or first-time adoption on an existing cache.
- Make `cache list` / `cache show` query SQLite when the index exists,
  falling back to the existing `Walk` implementation otherwise.

## Non-Goals

- Indexing remote storage tiers (GCS, S3). The index is **L1-only**.
  Cloud tiers have their own lifecycle policies and may be shared by
  many instances; a per-instance SQLite would be useless there.
- Background eviction sweeps. v1 is on-demand CLI only; cron handles
  scheduling outside the binary.
- Distributed coordination. SQLite is local; multiple proxy instances
  on the **same** machine sharing one cache dir use SQLite WAL mode and
  busy_timeout for concurrent access. Different machines maintain
  independent indexes for their own L1.
- Replacing `.meta` files. SQLite is an *index*; the meta JSON remains
  the source of truth for HTTP headers and any future fields.

## Architecture

### Component layout

```
internal/index/        new package
  index.go             type Index, Open/Close, schema, migrations
  ops.go               Insert/Touch/Delete/Get/List/EvictCandidates
  recorder.go          in-memory hit/access map + flush goroutine
  index_test.go        round-trip tests against a tempdir DB

internal/cache/cache.go
  Cache gets an optional *index.Index field
  Put: write storage tiers + Index.Insert
  Get: storage.Get → on hit, Index.Touch (in-memory)
       storage miss + index has row → Index.Delete (lazy reconcile)
       storage hit + index missing row → Index.Insert (lazy reconcile)
  Delete: storage.Delete + Index.Delete
  Walk: unchanged (storage-side fallback for tools without index)

cmd/escrow-proxy/cache.go
  cache list / cache show: query Index when available, else Walk
  cache evict: new subcommand
  cache reindex: new subcommand

cmd/escrow-proxy/main.go
  buildIndex(cfg) opens (or creates) the SQLite at <local-dir>/index.db
  startup hook: if cache dir non-empty and DB empty → auto-reindex once
  serve / record / offline pass the Index into the Cache
```

### Why a new package, not a wrapper around `storage.Storage`

The index needs `EntryMeta` (method, url, status), the cache key, and the
body size — none of which the `storage.Storage` interface carries. It
also conceptually tracks L1 only, while `storage.Storage` is tier-agnostic.
Wrapping local storage with an "indexed" decorator would force the
wrapper to parse meta JSON on every Put and entangle layers. A separate
`internal/index` package owned by the cache layer is cleaner.

### Driver choice

Use **`modernc.org/sqlite`** (pure-Go, no CGO). The project ships a
distroless container; CGO would force `libc` and complicate cross-compile.
`modernc.org/sqlite` is the de facto pure-Go SQLite for Go services.

## Schema

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS entries (
    key              TEXT PRIMARY KEY,
    method           TEXT NOT NULL,
    url              TEXT NOT NULL,
    status           INTEGER NOT NULL,
    body_size        INTEGER NOT NULL,
    created_at       INTEGER NOT NULL,        -- unix seconds
    last_accessed_at INTEGER NOT NULL,
    hit_count        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_entries_url             ON entries(url);
CREATE INDEX IF NOT EXISTS idx_entries_last_accessed   ON entries(last_accessed_at);

CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);
INSERT OR IGNORE INTO schema_version VALUES (1);
```

`url` index supports `=` and prefix `LIKE 'http://%'` queries.
`last_accessed_at` index supports eviction's `ORDER BY ... ASC LIMIT N`.

Schema migrations: simple integer version. v1 is the only version
shipped; future versions add columns/indexes via numbered migrations
applied in order based on the stored version.

## In-Memory Hit Map + Hybrid Flush

Updating `last_accessed_at` and `hit_count` on every cache hit through
SQLite would put a serialized writer lock on the request hot path. The
Index keeps a `map[string]hitDelta` in memory:

```go
type hitDelta struct {
    HitCount       int
    LastAccessedAt int64
}
```

`Touch(key)` updates the map under a mutex (no SQL).

A goroutine drains the map under three conditions, whichever fires first:

- **Timer**: every 30 seconds.
- **Threshold**: when the map has ≥ 1000 dirty entries.
- **Shutdown**: triggered explicitly by the proxy's shutdown hook.

Drain takes the map under lock, swaps in a fresh map, and writes a single
SQL transaction:

```sql
UPDATE entries
SET last_accessed_at = MAX(last_accessed_at, ?),
    hit_count        = hit_count + ?
WHERE key = ?
```

Lost updates on crash are bounded by the timer interval and by definition
acceptable — these stats are eviction signals, not correctness data.

## Lazy Reconciliation

On `Cache.Get(key)`:

| storage state | index state | action |
|---|---|---|
| present | row present | normal hit; `Index.Touch(key)` |
| present | row missing | hit; `Index.Insert(...)` from meta + sized body, then `Touch` |
| missing | row present | miss; `Index.Delete(key)` to drop the orphan row |
| missing | row missing | miss; nothing to reconcile |

Reconciliation is triggered only on observed access, so cold/orphaned
entries persist until either a hit or a manual `cache reindex`.

`cache list` and `cache show` queries will *not* trigger reconcile in v1
— they reflect the index state. Operators run `cache reindex` if they
suspect drift.

## CLI: `cache evict`

```
escrow-proxy cache evict [flags]

Flags:
  --target-size <bytes>   evict until total body bytes <= this value (required)
  --min-age <duration>    skip entries accessed more recently than this
  --dry-run               print what would be evicted, don't delete
  --json                  emit per-entry JSON instead of columns
```

Algorithm:

```
cur = SELECT COALESCE(SUM(body_size), 0) FROM entries
if cur <= target_size: print "nothing to evict (current=cur)" and exit
need = cur - target_size

cutoff = now - min_age   (or 0 if --min-age not set)

freed, deleted = 0, 0
for row in (SELECT key, body_size, method, url
            FROM entries
            WHERE last_accessed_at < cutoff
            ORDER BY last_accessed_at ASC):
    if freed >= need: break
    if not dry_run:
        # Cache here is built over the local tier only; Delete therefore
        # touches only L1 storage (both .meta and .body).
        Cache.Delete(ctx, key)
        Index.Delete(key)
    freed += body_size
    deleted += 1
    print "<key> <method> <body-bytes> <url>"

print "evicted N entries (M bytes); current=cur-freed target=target_size"
```

`--target-size` accepts SI/IEC suffixes via a parser: `150G`, `100M`,
`1.5T`. `--min-age` parses Go's `time.Duration` syntax (`24h`, `7d` —
note: Go does not support `d`, so the parser handles `d`/`w` manually).

**Eviction targets L1 only.** The CLI builds a `Cache` over the local
storage tier specifically (filter `cfg.Storage.Tiers` to type `local`),
so eviction never reaches into GCS/S3. If no local tier is configured,
the command exits with an error.

If `--target-size` is larger than current size and `--dry-run` not set,
exit 0 with a message — not an error.

## CLI: `cache reindex`

```
escrow-proxy cache reindex [flags]

Flags:
  --dry-run    report what would change, do not modify the index
```

Algorithm:

```
seen = {}
for key in Cache.Walk():
    seen[key] = true
    meta := from .meta
    body_size := storage.Size(key.body)
    Index.Upsert(key, meta, body_size, ...)
        on insert: created_at = now, last_accessed_at = now, hit_count = 0
        on update: refresh method/url/status/body_size only;
                   preserve created_at, last_accessed_at, hit_count

for row in (SELECT key FROM entries):
    if row.key not in seen:
        Index.Delete(row.key)

print summary: inserted=A updated=B removed=C
```

`Upsert`'s preservation of usage stats is important — reindex is a
correction tool, not a stat reset. A reindexed entry that's been hot
remains hot.

## Auto-reindex on first boot

In `serve` startup, after opening the index:

```go
count := Index.Count()
empty := count == 0

cacheNonEmpty := has any *.meta in <local-dir>

if empty && cacheNonEmpty:
    log "auto-reindex: index empty but cache has entries; rebuilding"
    if err := Cache.Reindex(ctx); err != nil:
        log warning "auto-reindex failed; continuing with empty index, run cache reindex"
```

This handles the upgrade path for the existing 200 G cache. After v1
ships, proxies started with an existing local cache populate the index
on first launch. Subsequent launches are fast no-ops.

## Switching `cache list` / `cache show` to SQL

When `Index` is non-nil:

- `cache list --url X [--method M]`:
  `SELECT key, method, url, status, body_size FROM entries
   WHERE url = ? [AND method = ?] LIMIT ?`
- `cache list --url-prefix X [--method M]`:
  `SELECT ... FROM entries
   WHERE url LIKE ? || '%' [AND method = ?] LIMIT ?`
- `cache list --key K`:
  `SELECT ... WHERE key = ?`
- `cache list` no filter:
  `SELECT ... FROM entries LIMIT ?` (default 1000)
- `cache show --key K`: `SELECT ... WHERE key = ?` then read meta from
  storage for the full headers (since headers aren't indexed).
- `cache show --url X`: `SELECT key FROM entries WHERE url = ?`,
  ambiguity error if more than one row, then load meta from storage.

When `Index` is nil (operator passed `--no-index`, or the open failed):
fall back to the existing `Walk` path.

## Configuration

New CLI flags on `serve`, `record`, and the `cache` subcommands:

```
--index-db <path>    SQLite path; default <local-dir>/index.db
--no-index           disable index entirely; falls back to Walk
```

Config file (`escrow-proxy.yaml`):

```yaml
cache:
  index:
    enabled: true        # default; --no-index sets false
    path: ""             # empty = <local-dir>/index.db
    flush_interval: 30s
    flush_threshold: 1000
```

CLI flags override config, as elsewhere.

## Concurrency

- SQLite WAL mode + `busy_timeout = 5000` allows multiple readers
  (CLI `cache list`, `cache show`) concurrent with one writer (`serve`).
- The serve process holds the long-lived writer goroutine (flush loop).
  CLI processes are short-lived: they open, query/mutate, close.
- Eviction holds the writer for the duration of the deletes. With
  WAL + busy_timeout, brief contention is OK; long blocks log a warning.

## Testing

Match existing patterns. New test files:

- `internal/index/index_test.go`
  - Open creates schema; reopen is idempotent
  - Insert + Get round-trip
  - Touch updates last_accessed_at and hit_count via flush
  - EvictCandidates returns rows in last_accessed_at ASC, respects min_age
  - Delete removes the row
  - WAL mode is set after Open

- `internal/index/recorder_test.go`
  - Hit map accumulates; flush drains and writes correct SQL
  - Threshold flush triggers when map exceeds N
  - Shutdown flush drains pending entries

- `internal/cache/cache_test.go` (extend)
  - Cache with Index: Put inserts row
  - Cache with Index: Get from L1 calls Touch
  - Lazy reconcile: storage missing + row → row deleted on Get
  - Lazy reconcile: storage present + no row → row inserted on Get
  - Cache without Index (nil): existing tests still pass

- `cmd/escrow-proxy/cache_test.go` (extend)
  - `runEvict`: target larger than current → no-op exit 0
  - `runEvict`: LRU order respected; min_age filters out fresh entries
  - `runEvict --dry-run`: storage untouched, index untouched
  - `runEvict`: deletes from L1 only (multi-tier setup with mock L2 untouched)
  - `runReindex`: missing entries inserted, orphan rows removed,
    usage stats preserved on update
  - `runReindex --dry-run`: counts reported, DB unchanged
  - `runList`/`runShow` query SQL when index is non-nil; observable
    via faster + indexed semantics (e.g., URL with no row returns nothing
    even if file exists on disk and lazy reconcile hasn't run)

All tests use `t.TempDir()` for the cache dir and an in-process SQLite
file (no in-memory shared cache; we want WAL semantics matching prod).

## File Touches

New:
- `internal/index/index.go`
- `internal/index/ops.go`
- `internal/index/recorder.go`
- `internal/index/index_test.go`
- `internal/index/recorder_test.go`

Modified:
- `internal/cache/cache.go` — optional `*index.Index` field, Put/Get/Delete
  hooks, lazy reconcile, `Reindex(ctx)` method
- `internal/cache/cache_test.go` — index integration tests
- `cmd/escrow-proxy/cache.go` — switch list/show to SQL when available;
  add `runEvict`, `runReindex`, `newCacheEvictCmd`, `newCacheReindexCmd`
- `cmd/escrow-proxy/cache_test.go` — evict / reindex tests
- `cmd/escrow-proxy/main.go` — wire the index in serve/record startup,
  auto-reindex hook, `--index-db` and `--no-index` flags
- `internal/config/config.go` — `cache.index` block
- `go.mod` / `go.sum` — `modernc.org/sqlite`
- `README.md` — usage examples for `evict`, `reindex`, and the index
- `docs/configuration.md` — index settings
- `docs/architecture.md` — note the index layer

## Open Questions

None. Ready for implementation.
