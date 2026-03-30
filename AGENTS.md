# Agents Guide

Instructions for AI coding agents working on this project.

## Project Overview

Escrow Proxy is a Go MITM HTTP/HTTPS caching proxy for CI/CD dependency management. It intercepts TLS traffic, caches responses by content digest in tiered storage, and supports three modes: `serve`, `record`, and `offline`.

## Build & Test

```bash
# Build
go build -o escrow-proxy ./cmd/escrow-proxy

# Run all tests
go test ./...

# Run tests for a specific package
go test ./internal/storage/...
go test ./internal/archive/...

# Run with verbose output
go test -v -run TestName ./internal/proxy/
```

## Project Structure

```
cmd/escrow-proxy/main.go    # CLI entrypoint (Cobra), wires all layers together
internal/
  proxy/                     # MITM proxy engine + request/response handler
  cache/                     # Cache layer (get/put), recorder, archive storage adapter
  storage/                   # Pluggable storage backends (local, GCS, S3, tiered)
  archive/                   # Portable archive formats (tar.gz, OCI, CAS)
  tls/                       # CA and leaf certificate management
  config/                    # YAML config parsing + CLI flag merging
```

All application code lives under `internal/` — nothing is exported as a library.

## Key Interfaces

### Storage (`internal/storage/storage.go`)

```go
type Storage interface {
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Put(ctx context.Context, key string, r io.Reader) error
    Exists(ctx context.Context, key string) (bool, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]string, error)
}
```

Use `storage.ErrNotFound` for key-not-found errors.

### Archive (`internal/archive/archive.go`)

```go
type Writer interface {
    Add(ctx context.Context, key string, meta []byte, body io.Reader) error
    Close() error
}

type Reader interface {
    Get(ctx context.Context, key string) ([]byte, io.ReadCloser, error)
    List(ctx context.Context) ([]string, error)
    Close() error
}

type Format interface {
    NewWriter(dest string) (Writer, error)
    NewReader(src string) (Reader, error)
}
```

### Cache Entry (`internal/cache/entry.go`)

Each cached response is stored as two objects under the cache key digest:
- `{digest}.meta` — JSON: `{"method", "url", "status_code", "header"}`
- `{digest}.body` — raw response body bytes

## How to Add a New Storage Backend

1. **Create `internal/storage/mybackend.go`** implementing the `Storage` interface
2. **Add constructor**: `func NewMyBackend(ctx context.Context, ...) (*MyBackend, error)`
3. **Register in CLI**: Add a `case "mybackend":` to the `buildStorage()` switch in `cmd/escrow-proxy/main.go`
4. **Add config fields**: Add the backend's config to `config.StorageTierConfig` in `internal/config/config.go`
5. **Add CLI flags**: Wire up flags in the Cobra command setup in `main.go`
6. **Write tests**: Create `internal/storage/mybackend_test.go` with the same patterns as `local_test.go` — use real resources or emulators, not mocks
7. **Update docs**: Add the backend to `docs/configuration.md` and `README.md`

## How to Add a New Archive Format

1. **Create `internal/archive/myformat.go`** implementing `Format`, `Writer`, and `Reader`
2. **Register in factory**: Add a `case "myformat":` to `NewFormat()` in `internal/archive/detect.go`
3. **Add detection rule**: Update `DetectFormat()` in `detect.go` if the format has a recognizable path pattern
4. **Write tests**: Create `internal/archive/myformat_test.go` — must include a round-trip test (write entries → read back → verify identical content)
5. **Update docs**: Add the format to `docs/archive-formats.md` and `README.md`

## Conventions

### Error Handling
- Only cache HTTP responses with status 200-399
- Storage write failures on L2 should not block serving from L1 — log as warning
- Use `storage.ErrNotFound` consistently for missing keys
- Offline mode returns HTTP 502 on cache miss (unless `--allow-fallback`)

### Testing
- **No mocks for the storage interface** in integration tests — use real local storage and emulators for cloud backends
- Archive tests must be round-trip: write → read → verify
- Proxy tests use `httptest` servers as upstream
- Use temp directories (`t.TempDir()`) for local storage in tests

### Cache Keys
- Computed as `SHA256(method + "\n" + url + "\n" + sorted_headers + "\n" + body_hash)`
- Headers are sorted by name, then values sorted, formatted as `name:value`
- Body hash is SHA256 of request body (empty string hash if no body)

### Streaming
- Large responses are streamed via `io.TeeReader` — never buffer entire responses in memory
- Storage `Get` returns `io.ReadCloser` — callers must close it
- Storage `Put` accepts `io.Reader` — implementations should stream, not buffer

### Registration Pattern
- All extension points (storage backends, archive formats) use **explicit switch-based registration** in their respective factory functions
- No init hooks, reflection, or plugin systems

### Config Precedence
- CLI flags override YAML config file values
- Config struct fields use `yaml` tags for deserialization
