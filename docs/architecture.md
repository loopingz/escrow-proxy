# Architecture

## Overview

Escrow Proxy is organized into four layers, each with a clean interface boundary:

```
┌─────────────────────────────────────────────┐
│                   CLI Layer                  │
│          cmd/escrow-proxy/main.go            │
│     (Cobra commands: serve/record/offline)   │
├─────────────────────────────────────────────┤
│                 Proxy Engine                 │
│              internal/proxy/                 │
│   (MITM TLS interception, request routing)  │
├─────────────────────────────────────────────┤
│                 Cache Layer                  │
│              internal/cache/                 │
│   (Content-addressed get/put, recording)    │
├──────────────────┬──────────────────────────┤
│  Storage Layer   │     Archive Layer        │
│ internal/storage │   internal/archive/      │
│ (local,GCS,S3,   │  (tar.gz, OCI, CAS)     │
│  tiered)         │                          │
└──────────────────┴──────────────────────────┘
```

## Layers

### 1. CLI Layer (`cmd/escrow-proxy/`)

Built with [Cobra](https://github.com/spf13/cobra). Parses flags, loads YAML config, wires up dependencies, and starts the proxy.

Subcommands:
- `serve` — normal caching proxy
- `record` — caching proxy + archive writer on shutdown
- `offline` — read-only replay from archive
- `ca export` — print CA certificate PEM

### 2. Proxy Engine (`internal/proxy/`)

Built on [goproxy](https://github.com/elazarl/goproxy). Handles:

- **CONNECT interception** — intercepts HTTPS CONNECT requests and performs TLS man-in-the-middle with dynamically generated certificates
- **Cache key computation** — deterministic SHA256 digest of method, URL, selected headers, and body hash
- **Request handling** — checks cache before forwarding upstream; caches responses (2xx-3xx only)
- **Response streaming** — uses `io.TeeReader` to avoid buffering entire responses in memory

### 3. Cache Layer (`internal/cache/`)

Manages cached entries as pairs of objects:
- `{digest}.meta` — JSON: method, URL, status code, response headers
- `{digest}.body` — raw response body

Components:
- **Cache** — core get/put logic against storage
- **Recorder** — wraps Cache, tracks all written keys for archive finalization
- **ArchiveStorage** — read-only storage backed by an archive reader (used in offline mode)

### 4. Storage Layer (`internal/storage/`)

Pluggable interface:

```go
type Storage interface {
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Put(ctx context.Context, key string, r io.Reader) error
    Exists(ctx context.Context, key string) (bool, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]string, error)
}
```

Implementations:
- **Local** — filesystem
- **GCS** — Google Cloud Storage
- **S3** — AWS S3
- **Tiered** — ordered list of backends with read-through backfill

### 5. Archive Layer (`internal/archive/`)

Portable serialization of cache contents:

```go
type ArchiveWriter interface {
    Add(ctx context.Context, key string, meta []byte, body io.Reader) error
    Close() error
}

type ArchiveReader interface {
    Get(ctx context.Context, key string) ([]byte, io.ReadCloser, error)
    List(ctx context.Context) ([]string, error)
    Close() error
}
```

Formats:
- **tar.gz** — single compressed file with flat entry list
- **OCI** — OCI Image Layout with chunked layers, registry push/pull via [oras-go](https://github.com/oras-project/oras-go)
- **CAS** — content-addressed directory with deduplication

## TLS (`internal/tls/`)

- **CA management** — auto-generate or load custom root CA (ECDSA P-256)
- **Leaf certificates** — generated per-host on CONNECT, 24-hour validity, LRU cached in memory

## Request Flow

### Serve Mode

```
Client → CONNECT → MITM TLS → Parse Request
  → Compute cache key (SHA256 digest)
  → Check L1 (local) → hit? return cached response
  → Check L2 (GCS/S3) → hit? backfill L1, return cached response
  → Miss → forward upstream (with configurable timeout)
  → Cache response (2xx-3xx only) in all tiers concurrently
  → Return response to client
```

### Record Mode

Same as serve, plus:
- Each cached key is recorded in a session manifest
- On SIGINT/SIGTERM: reads all recorded entries from storage, writes them to the chosen archive format, then exits

### Offline Mode

```
Client → CONNECT → MITM TLS → Parse Request
  → Compute cache key
  → Check archive index → hit? return archived response
  → Miss → return HTTP 502 (or forward upstream if --allow-fallback)
```

## Error Handling

| Scenario | Behavior |
|----------|----------|
| Upstream 5xx | Not cached; response forwarded to client as-is |
| L2 write failure | Response still served from L1; failure logged as warning |
| All tiers miss | Forward upstream (serve/record) or 502 (offline) |
| Large responses | Streamed via `io.TeeReader`, no full memory buffering |
| Concurrent duplicate requests | Deduplicated via `singleflight` |
| Graceful shutdown | Finish in-flight requests, flush writes, finalize archive |
