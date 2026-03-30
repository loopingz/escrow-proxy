# Escrow Proxy

A MITM HTTP/HTTPS caching proxy for CI/CD dependency management. Escrow Proxy intercepts TLS traffic, caches responses by content digest in tiered storage (local + cloud), and supports three operating modes: normal caching, session recording to portable archives, and offline replay.

## Use Cases

- **Cache package downloads** (npm, Maven, pip, Docker images) to avoid repeated external registry hits
- **Record CI/CD sessions** into portable archives for reproducibility
- **Offline/air-gapped builds** by replaying cached responses from archives
- **Tiered caching** with fast local L1 and cloud L2 (GCS, S3)

## Installation

```bash
go install github.com/loopingz/escrow-proxy/cmd/escrow-proxy@latest
```

Or build from source:

```bash
git clone https://github.com/loopingz/escrow-proxy.git
cd escrow-proxy
go build -o escrow-proxy ./cmd/escrow-proxy
```

## Quick Start

### 1. Start the proxy

```bash
escrow-proxy serve
```

This starts a caching proxy on `:8080` with local storage at `~/.escrow-proxy/cache/`. On first run, a root CA is auto-generated at `~/.escrow-proxy/`.

### 2. Trust the CA certificate

Export and install the CA certificate so your tools trust the proxy:

```bash
escrow-proxy ca export > escrow-proxy-ca.crt

# macOS
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain escrow-proxy-ca.crt

# Linux (Debian/Ubuntu)
sudo cp escrow-proxy-ca.crt /usr/local/share/ca-certificates/
sudo update-ca-certificates

# Or set environment variables for specific tools
export NODE_EXTRA_CA_CERTS=escrow-proxy-ca.crt
export REQUESTS_CA_BUNDLE=escrow-proxy-ca.crt
```

### 3. Configure your tools to use the proxy

```bash
export HTTP_PROXY=http://localhost:8080
export HTTPS_PROXY=http://localhost:8080

# Now package managers will go through the cache
npm install
pip install -r requirements.txt
mvn dependency:resolve
```

## Modes

### Serve (default)

Normal transparent caching proxy. Requests are forwarded upstream on cache miss, and responses are cached in tiered storage.

```bash
escrow-proxy serve --listen :8080 --storage local,gcs --gcs-bucket my-cache
```

### Record

Like `serve`, but additionally records all cached entries into a portable archive on shutdown (SIGINT/SIGTERM).

```bash
# Record to a tar.gz file
escrow-proxy record -o build-deps.tar.gz

# Record to an OCI registry
escrow-proxy record -o registry.example.com/cache:v1

# Record to a CAS directory
escrow-proxy record -o ./cache-snapshot/ --format cas
```

### Offline

Serves responses exclusively from a previously recorded archive. Returns HTTP 502 on cache miss (unless `--allow-fallback` is set).

```bash
# From a tar.gz file
escrow-proxy offline -a build-deps.tar.gz

# From an OCI registry
escrow-proxy offline -a registry.example.com/cache:v1

# Allow upstream fallback on miss
escrow-proxy offline -a build-deps.tar.gz --allow-fallback
```

## Request Flow

```
Client Request
  → CONNECT → MITM TLS Intercept
  → Compute cache key: SHA256(method + url + headers + body_hash)
  → Check L1 (local) → hit? → return cached response
  → Check L2 (GCS/S3) → hit? → backfill L1 + return cached response
  → Miss → forward upstream (with timeout)
  → Cache response (2xx-3xx only) to all tiers
  → Return response to client
```

## Storage Backends

Escrow Proxy supports pluggable, tiered storage. Tiers are checked in order on read; writes propagate to all tiers concurrently.

### Local (default)

Files on disk under a configurable directory.

```bash
escrow-proxy serve --storage local --local-dir /tmp/escrow-cache
```

### Google Cloud Storage

```bash
escrow-proxy serve --storage local,gcs --gcs-bucket my-bucket --gcs-prefix escrow/
```

Credentials are auto-detected from the environment (Application Default Credentials).

### AWS S3

```bash
escrow-proxy serve --storage local,s3 --s3-bucket my-bucket --s3-prefix escrow/ --s3-region us-west-2
```

Credentials are auto-detected from the AWS SDK defaults.

### Tiered Storage

Combine backends for performance + durability:

```bash
escrow-proxy serve --storage local,gcs --local-dir /tmp/fast-cache --gcs-bucket durable-cache
```

- **Read**: L1 checked first; L2 hit triggers backfill to L1
- **Write**: All tiers written concurrently

## Archive Formats

### tar.gz

Single portable file. Best for simple use cases.

```
archive.tar.gz
├── index.json
├── {digest}.meta
├── {digest}.body
└── ...
```

### OCI Image

Registry-compatible format using [OCI Image Layout](https://github.com/opencontainers/image-spec). Supports push/pull to any OCI-compliant registry (Docker Hub, ECR, GCR, etc.).

```bash
# Record and push to registry
escrow-proxy record -o registry.example.com/deps:v1 --oci-entries-per-layer 500

# Pull and serve offline
escrow-proxy offline -a registry.example.com/deps:v1
```

### Custom CAS (Content-Addressed Storage)

Directory-based format with content deduplication. Easy to inspect and diff.

```
cas/
├── index.json
├── blobs/sha256/
│   └── ...
└── meta/
    └── ...
```

### Format Auto-Detection

The format is detected from the output/archive path:

| Path | Detected Format |
|------|----------------|
| `*.tar.gz` / `*.tgz` | tar.gz |
| `registry.example.com/repo:tag` | OCI |
| `./path/` | CAS |

Override with `--format={tgz,oci,cas}`.

## Configuration

### Config File (YAML)

```yaml
listen: ":8080"
log_level: "info"
upstream_timeout: 30s

ca:
  cert: /path/to/ca.crt
  key: /path/to/ca.key

cache:
  key_headers:
    - Accept
    - Accept-Encoding

storage:
  tiers:
    - type: local
      dir: /tmp/escrow-cache
    - type: gcs
      bucket: my-bucket
      prefix: escrow/
    - type: s3
      bucket: my-bucket
      prefix: escrow/
      region: us-west-2

record:
  output: registry.example.com/cache:latest
  format: oci
  oci_entries_per_layer: 1000

offline:
  archive: registry.example.com/cache:latest
  allow_fallback: false
```

```bash
escrow-proxy serve --config config.yaml
```

CLI flags override config file values.

### CLI Reference

#### Common Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | | Path to YAML config file |
| `--listen` / `-l` | `:8080` | Bind address |
| `--ca-cert` / `--ca-key` | | Custom CA certificate and key paths |
| `--cache-key-headers` | `Accept,Accept-Encoding` | Headers included in cache key |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--upstream-timeout` | `30s` | Timeout for upstream requests |

#### Storage Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--storage` | `local` | Comma-separated tier list (e.g., `local,gcs`) |
| `--local-dir` | `~/.escrow-proxy/cache/` | Local cache directory |
| `--gcs-bucket` | | GCS bucket name |
| `--gcs-prefix` | | GCS key prefix |
| `--s3-bucket` | | S3 bucket name |
| `--s3-prefix` | | S3 key prefix |
| `--s3-region` | | S3 region |

#### Record Mode Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--output` / `-o` | | Archive destination (required) |
| `--format` | auto-detect | `tgz`, `oci`, or `cas` |
| `--oci-entries-per-layer` | `1000` | Entries per OCI layer |

#### Offline Mode Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--archive` / `-a` | | Archive source path or registry ref (required) |
| `--allow-fallback` | `false` | Forward to upstream on cache miss instead of returning 502 |

## TLS & Certificate Management

- **Auto-generated CA**: On first run, a root CA (ECDSA P-256, 10-year validity) is created at `~/.escrow-proxy/`
- **Custom CA**: Provide your own via `--ca-cert` and `--ca-key`
- **Leaf certificates**: Generated dynamically per-host, signed by the CA, with 24-hour validity and LRU caching
- **Export**: `escrow-proxy ca export` prints the CA PEM to stdout

## CI/CD Examples

### GitHub Actions

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Start escrow-proxy
        run: |
          escrow-proxy serve --storage local --local-dir ${{ runner.temp }}/escrow-cache &
          escrow-proxy ca export > /tmp/escrow-ca.crt
          echo "HTTP_PROXY=http://localhost:8080" >> $GITHUB_ENV
          echo "HTTPS_PROXY=http://localhost:8080" >> $GITHUB_ENV
          echo "NODE_EXTRA_CA_CERTS=/tmp/escrow-ca.crt" >> $GITHUB_ENV

      - run: npm ci
```

### Recording and Replaying Dependencies

```bash
# Step 1: Record a clean build
escrow-proxy record -o deps-v1.tar.gz &
PROXY_PID=$!
export HTTPS_PROXY=http://localhost:8080
npm ci && mvn package
kill $PROXY_PID  # triggers archive finalization

# Step 2: Replay offline
escrow-proxy offline -a deps-v1.tar.gz &
export HTTPS_PROXY=http://localhost:8080
npm ci && mvn package  # served entirely from cache
```

## Development

### Prerequisites

- Go 1.25+

### Build

```bash
go build -o escrow-proxy ./cmd/escrow-proxy
```

### Test

```bash
go test ./...
```

### Project Structure

```
escrow-proxy/
├── cmd/escrow-proxy/       # CLI entrypoint (Cobra)
│   └── main.go
├── internal/
│   ├── proxy/              # MITM proxy engine + request handler
│   ├── cache/              # Cache layer, recorder, archive storage
│   ├── storage/            # Pluggable backends (local, GCS, S3, tiered)
│   ├── archive/            # Archive formats (tar.gz, OCI, CAS)
│   ├── tls/                # CA and leaf certificate management
│   └── config/             # YAML config parsing
├── docs/                   # Design documentation
├── go.mod
└── go.sum
```

## License

TBD
