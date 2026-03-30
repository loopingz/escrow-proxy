# Archive Formats

Escrow Proxy supports three archive formats for recording and replaying cached dependencies. The format is auto-detected from the destination path, or can be explicitly set with `--format`.

## Format Selection

| Path Pattern | Detected Format |
|-------------|----------------|
| `*.tar.gz` / `*.tgz` | tar.gz |
| `registry.example.com/repo:tag` | OCI |
| `./directory/` or relative path | CAS |

Override with `--format={tgz,oci,cas}`.

## tar.gz

A single compressed archive file containing all cached entries. Best for simple workflows and file-based sharing.

### Structure

```
archive.tar.gz
├── index.json          # List of entry keys
├── {digest1}.meta      # JSON: method, URL, status, headers
├── {digest1}.body      # Raw response body
├── {digest2}.meta
├── {digest2}.body
└── ...
```

### Usage

```bash
# Record
escrow-proxy record -o deps.tar.gz

# Replay
escrow-proxy offline -a deps.tar.gz
```

### Characteristics

- Single portable file
- Entire archive loaded into memory on read
- Best for small-to-medium dependency sets

## OCI Image

[OCI Image Layout](https://github.com/opencontainers/image-spec) format. Entries are grouped into chunked tar layers. Supports native push/pull to any OCI-compliant container registry.

### Structure

```
oci-layout/
├── oci-layout           # {"imageLayoutVersion": "1.0.0"}
├── index.json           # OCI index pointing to manifest
└── blobs/sha256/
    ├── {manifest}       # OCI manifest descriptor
    ├── {config}         # Config blob containing entry index
    ├── {layer1}.tar     # Tar of ~N entries (meta + body pairs)
    └── {layer2}.tar     # Next batch
```

### Usage

```bash
# Record and push to registry
escrow-proxy record -o registry.example.com/deps:v1

# Control layer size
escrow-proxy record -o registry.example.com/deps:v1 --oci-entries-per-layer 500

# Pull and replay
escrow-proxy offline -a registry.example.com/deps:v1
```

### Characteristics

- Registry-compatible (Docker Hub, ECR, GCR, GHCR, etc.)
- Chunked layers avoid single-blob size limits
- Uses [oras-go](https://github.com/oras-project/oras-go) for registry operations
- Uses standard Docker/OCI registry authentication (docker config, environment variables)
- `--oci-entries-per-layer` controls entries per layer (default: 1000)

## CAS (Content-Addressed Storage)

A directory-based format where response bodies are stored by their content hash. Provides natural deduplication and is easy to inspect.

### Structure

```
cas-root/
├── index.json           # key → {meta_digest, body_digest}
├── blobs/sha256/
│   ├── {sha256-of-body1}
│   ├── {sha256-of-body2}
│   └── ...
└── meta/
    ├── {key1}.json      # Entry metadata
    └── {key2}.json
```

### Usage

```bash
# Record to directory
escrow-proxy record -o ./cache-snapshot/ --format cas

# Replay from directory
escrow-proxy offline -a ./cache-snapshot/
```

### Characteristics

- Human-readable directory structure
- Content-addressed blobs (automatic deduplication)
- Easy to inspect, diff, and version control
- Not a single file — requires directory access

## Choosing a Format

| Criteria | tar.gz | OCI | CAS |
|----------|--------|-----|-----|
| Single file | Yes | No (directory or registry) | No (directory) |
| Registry support | No | Yes | No |
| Deduplication | No | No | Yes |
| Human-readable | No | No | Yes |
| Large archives | Memory-limited | Chunked layers | Scales well |
| Portability | High (any filesystem) | High (any registry) | Medium (directory) |
