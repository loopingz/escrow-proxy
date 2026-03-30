# CI/CD Integration Guide

## Overview

Escrow Proxy fits into CI/CD pipelines as a transparent caching layer between your build tools and external package registries. It requires:

1. Starting the proxy process
2. Trusting the CA certificate
3. Setting `HTTP_PROXY` / `HTTPS_PROXY` environment variables

## GitHub Actions

### Basic Caching

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Setup escrow-proxy
        run: |
          # Install (or use a pre-built binary)
          go install github.com/loopingz/escrow-proxy/cmd/escrow-proxy@latest

          # Start proxy in background
          escrow-proxy serve --local-dir ${{ runner.temp }}/escrow-cache &

          # Trust the CA
          escrow-proxy ca export | sudo tee /usr/local/share/ca-certificates/escrow-proxy.crt
          sudo update-ca-certificates

          # Configure proxy for subsequent steps
          echo "HTTP_PROXY=http://localhost:8080" >> $GITHUB_ENV
          echo "HTTPS_PROXY=http://localhost:8080" >> $GITHUB_ENV

      - run: npm ci
      - run: pip install -r requirements.txt
```

### Record + Replay Pattern

Use `record` mode to capture dependencies, then `offline` mode for hermetic builds.

```yaml
# Job 1: Record dependencies
record-deps:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - name: Record dependencies
      run: |
        escrow-proxy record -o deps-${{ github.sha }}.tar.gz &
        PROXY_PID=$!
        export HTTPS_PROXY=http://localhost:8080
        escrow-proxy ca export > /tmp/ca.crt
        export NODE_EXTRA_CA_CERTS=/tmp/ca.crt

        npm ci
        kill $PROXY_PID  # Triggers archive finalization

    - uses: actions/upload-artifact@v4
      with:
        name: deps-archive
        path: deps-${{ github.sha }}.tar.gz

# Job 2: Hermetic build using recorded deps
hermetic-build:
  needs: record-deps
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/download-artifact@v4
      with:
        name: deps-archive

    - name: Offline build
      run: |
        escrow-proxy offline -a deps-${{ github.sha }}.tar.gz &
        export HTTPS_PROXY=http://localhost:8080
        escrow-proxy ca export > /tmp/ca.crt
        export NODE_EXTRA_CA_CERTS=/tmp/ca.crt

        npm ci  # Served entirely from cache
```

### With OCI Registry

Store dependency archives in your container registry instead of artifacts.

```yaml
record-deps:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - name: Login to registry
      run: echo "${{ secrets.GITHUB_TOKEN }}" | docker login ghcr.io -u ${{ github.actor }} --password-stdin

    - name: Record and push
      run: |
        escrow-proxy record -o ghcr.io/${{ github.repository }}/deps:${{ github.sha }} &
        PROXY_PID=$!
        export HTTPS_PROXY=http://localhost:8080
        # ... install dependencies ...
        kill $PROXY_PID

hermetic-build:
  needs: record-deps
  runs-on: ubuntu-latest
  steps:
    - name: Offline build
      run: |
        escrow-proxy offline -a ghcr.io/${{ github.repository }}/deps:${{ github.sha }} &
        export HTTPS_PROXY=http://localhost:8080
        # ... build ...
```

## GitLab CI

```yaml
build:
  stage: build
  before_script:
    - escrow-proxy serve --local-dir /tmp/escrow-cache &
    - escrow-proxy ca export | sudo tee /usr/local/share/ca-certificates/escrow.crt
    - sudo update-ca-certificates
    - export HTTP_PROXY=http://localhost:8080 HTTPS_PROXY=http://localhost:8080
  script:
    - npm ci
    - go build ./...
```

## Docker Builds

### Build-Time Proxy

Pass the proxy as a build argument:

```dockerfile
# syntax=docker/dockerfile:1
FROM node:20 AS build
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NODE_EXTRA_CA_CERTS=/tmp/escrow-ca.crt
COPY escrow-ca.crt /tmp/escrow-ca.crt
RUN npm ci
```

```bash
escrow-proxy serve &
escrow-proxy ca export > escrow-ca.crt
docker build \
  --build-arg HTTP_PROXY=http://host.docker.internal:8080 \
  --build-arg HTTPS_PROXY=http://host.docker.internal:8080 \
  .
```

## Tool-Specific CA Trust

Different tools require different methods to trust the CA certificate:

| Tool | Method |
|------|--------|
| Node.js / npm | `NODE_EXTRA_CA_CERTS=/path/to/ca.crt` |
| Python / pip | `REQUESTS_CA_BUNDLE=/path/to/ca.crt` or `PIP_CERT=/path/to/ca.crt` |
| curl | `CURL_CA_BUNDLE=/path/to/ca.crt` or `--cacert` flag |
| Go | System trust store, or `SSL_CERT_FILE=/path/to/ca.crt` |
| Java / Maven | `keytool -importcert -file ca.crt -keystore $JAVA_HOME/lib/security/cacerts` |
| Ruby / Bundler | `SSL_CERT_FILE=/path/to/ca.crt` |
| System (Debian) | Copy to `/usr/local/share/ca-certificates/` + `update-ca-certificates` |
| System (RHEL) | Copy to `/etc/pki/ca-trust/source/anchors/` + `update-ca-trust` |
| System (macOS) | `security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt` |

## Cloud Storage with CI

### GCS in Google Cloud Build

```yaml
steps:
  - name: golang
    env:
      - GOOGLE_APPLICATION_CREDENTIALS=/workspace/sa-key.json
    args:
      - escrow-proxy
      - serve
      - --storage=local,gcs
      - --gcs-bucket=$_CACHE_BUCKET
```

### S3 in AWS CodeBuild

```yaml
phases:
  pre_build:
    commands:
      - escrow-proxy serve --storage local,s3 --s3-bucket $CACHE_BUCKET --s3-region $AWS_REGION &
      - export HTTPS_PROXY=http://localhost:8080
  build:
    commands:
      - npm ci
```

## Best Practices

1. **Use tiered storage** in CI: local L1 for speed within a job, cloud L2 for cross-job and cross-pipeline sharing
2. **Pin archive versions** by commit SHA or build ID to ensure reproducibility
3. **Use OCI format** when your CI already has registry access — avoids artifact upload/download
4. **Set `--upstream-timeout`** appropriately for your network — default 30s may be too short for large packages
5. **Pre-warm caches** by running a record session on a representative build, then sharing the archive
