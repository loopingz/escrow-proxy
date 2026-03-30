# Configuration Guide

Escrow Proxy can be configured via a YAML config file, CLI flags, or a combination of both. CLI flags take precedence over config file values.

## Config File

Pass a config file with `--config`:

```bash
escrow-proxy serve --config /etc/escrow-proxy/config.yaml
```

### Full Example

```yaml
# Bind address
listen: ":8080"

# Log level: debug, info, warn, error
log_level: "info"

# Timeout for upstream requests
upstream_timeout: 30s

# TLS CA configuration
ca:
  cert: /etc/escrow-proxy/ca.crt
  key: /etc/escrow-proxy/ca.key

# Cache key configuration
cache:
  # HTTP headers included in the cache key computation
  # Only these headers affect cache key differentiation
  key_headers:
    - Accept
    - Accept-Encoding

# Storage backend configuration
storage:
  tiers:
    # L1: Fast local cache
    - type: local
      dir: /var/cache/escrow-proxy

    # L2: Durable cloud storage (GCS)
    - type: gcs
      bucket: my-company-escrow-cache
      prefix: ci/

    # L2: Durable cloud storage (S3)
    - type: s3
      bucket: my-company-escrow-cache
      prefix: ci/
      region: us-west-2

# Record mode settings
record:
  # Archive destination (file path or registry reference)
  output: registry.example.com/escrow-cache:latest
  # Archive format: tgz, oci, cas (auto-detected if omitted)
  format: oci
  # Number of entries per OCI layer
  oci_entries_per_layer: 1000

# Offline mode settings
offline:
  # Archive source (file path or registry reference)
  archive: registry.example.com/escrow-cache:latest
  # Allow upstream fallback on cache miss (default: false, returns 502)
  allow_fallback: false
```

## Storage Configuration

### Local Storage

The default backend. Stores cached entries as files on disk.

```yaml
storage:
  tiers:
    - type: local
      dir: /tmp/escrow-cache  # Default: ~/.escrow-proxy/cache/
```

### Google Cloud Storage

Requires GCP credentials in the environment (Application Default Credentials, service account key, or Workload Identity).

```yaml
storage:
  tiers:
    - type: gcs
      bucket: my-bucket
      prefix: escrow/  # Optional key prefix
```

### AWS S3

Requires AWS credentials in the environment (environment variables, IAM role, or shared credentials file).

```yaml
storage:
  tiers:
    - type: s3
      bucket: my-bucket
      prefix: escrow/   # Optional key prefix
      region: us-west-2  # Required
```

### Tiered Storage

Combine multiple backends for performance + durability. Tiers are listed in priority order (L1 first).

```yaml
storage:
  tiers:
    - type: local
      dir: /tmp/fast-cache
    - type: gcs
      bucket: durable-cache
```

**Behavior:**
- **Read**: Tiers are checked in order. A hit at tier N backfills all tiers < N (promotes to faster tiers).
- **Write**: All tiers are written concurrently.
- **Delete**: Deletes from all tiers.

## Cache Key

The cache key is a SHA256 digest of:

```
SHA256(method + "\n" + url + "\n" + sorted_headers + "\n" + body_hash)
```

Where:
- `sorted_headers` includes only the configured headers (default: `Accept`, `Accept-Encoding`)
- `body_hash` is the SHA256 of the request body (empty string hash if no body)

Configure which headers are included:

```yaml
cache:
  key_headers:
    - Accept
    - Accept-Encoding
    - Authorization  # Include if different auth tokens should produce different cache entries
```

## TLS Configuration

### Auto-Generated CA (Default)

On first run with no CA configured, a root CA (ECDSA P-256, 10-year validity) is generated and stored in `~/.escrow-proxy/`.

### Custom CA

Provide your own CA for environments where you manage trust stores centrally:

```yaml
ca:
  cert: /etc/pki/escrow-proxy/ca.crt
  key: /etc/pki/escrow-proxy/ca.key
```

### Exporting the CA Certificate

```bash
# Print CA PEM to stdout
escrow-proxy ca export

# Save to file
escrow-proxy ca export > escrow-ca.crt
```

## Environment Variables

Storage backends use standard cloud SDK credential mechanisms:

| Backend | Credentials |
|---------|------------|
| GCS | `GOOGLE_APPLICATION_CREDENTIALS`, ADC, Workload Identity |
| S3 | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, IAM role, `~/.aws/credentials` |

The proxy itself is configured via `HTTP_PROXY` / `HTTPS_PROXY` environment variables on the client side.
