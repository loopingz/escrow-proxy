# Release Automation Design

Automate versioning, release notes, multi-platform binaries, and signed container images so a merge to `main` eventually produces a fully published GitHub Release and a published container image without human steps between the merge and the artifact.

## Goals

- Versioning and changelog driven by conventional commits, not manual edits.
- Every release produces binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`.
- Every release produces a multi-arch (`linux/amd64` + `linux/arm64`) Docker image on `ghcr.io/loopingz/escrow-proxy`.
- Artifacts are verifiable: SHA256 checksums, SBOMs, and cosign keyless signatures. Binaries are signed transitively via the signed `checksums.txt` (standard GoReleaser pattern); the Docker manifest is signed directly.
- No long-lived secrets. Everything runs through `GITHUB_TOKEN` and GitHub OIDC.

## Non-goals

- Regular CI (test/lint on PRs). The repo has none today; adding it is tracked separately.
- Publishing to Docker Hub, Homebrew, nFPM `.deb`/`.rpm`, or any channel beyond GHCR + GitHub Releases.
- Auto-bumping the Dockerfile base image. Dependabot/Renovate can be added later.
- Building release artifacts from forks or branches. Only tags created by release-please trigger the release workflow.

## Architecture

Two decoupled GitHub Actions workflows, connected only through GitHub's native `release: published` event — neither workflow reads outputs from the other.

```
Developer pushes conventional commit to main
         │
         ▼
┌─────────────────────────────┐
│ release-please.yml          │  trigger: push to main
│  - opens/updates Release PR │
│  - on Release PR merge:     │
│    • creates tag vX.Y.Z     │
│    • creates GitHub Release │
└──────────────┬──────────────┘
               │ release.published event
               ▼
┌─────────────────────────────┐
│ release.yml                 │  trigger: release published
│  GoReleaser:                │
│   • builds 5 binaries       │
│   • generates checksums     │
│   • builds multi-arch image │
│   • pushes to ghcr.io       │
│   • cosign-signs both       │
│   • generates SBOM          │
│   • uploads to the Release  │
└─────────────────────────────┘
```

### Why two workflows instead of one

- release-please must run on *every* push to `main` (it maintains the Release PR). GoReleaser must run *only* when a tag is cut. Splitting keeps triggers clean.
- The two concerns evolve separately: changing the changelog format does not touch binary builds; adding a new platform does not touch commit conventions.
- Failure of the release job does not roll back the tag. A failed release can be re-run from the Actions UI.

## Files

### New files

| File | Purpose |
|------|---------|
| `.github/workflows/release-please.yml` | Runs `googleapis/release-please-action@v4` on pushes to `main`. |
| `.github/workflows/release.yml` | Runs GoReleaser on `release: published`. |
| `.release-please-config.json` | release-please config. |
| `.release-please-manifest.json` | Tracks current version. Initialized to `{ ".": "0.1.0" }`. |
| `.goreleaser.yaml` | GoReleaser config: builds, archives, checksums, SBOM, dockers, signs, release. |
| `Dockerfile` | Minimal image: `FROM gcr.io/distroless/static-debian12:nonroot`, copies the pre-built binary GoReleaser injects via its build context. |
| `.dockerignore` | Keep the build context small (exclude `.git`, `dist/`, tests). |

### Modified files

| File | Change |
|------|--------|
| `cmd/escrow-proxy/main.go` | Add `var (version, commit, date string)` at package scope and a `--version` flag on the root command that prints `escrow-proxy {version} ({commit}, {date})`. GoReleaser's `-X` ldflags populate these at build time. |
| `README.md` | Install section gains `docker pull ghcr.io/loopingz/escrow-proxy:<version>` and a `cosign verify` snippet. |
| `.gitignore` | Add `dist/` (GoReleaser's output dir). |

## Configuration details

### `.release-please-config.json`

- `release-type: go` — thin Go-specific variant; adjusts the changelog and does not rewrite `go.mod`.
- `packages: { ".": { "package-name": "escrow-proxy" } }` — single-package repo.
- `include-component-in-tag: false` — tags are plain `v0.1.0`, not `escrow-proxy-v0.1.0`.
- `changelog-sections` — conventional-commit sections with `hidden: true` for `chore`, `test`, `refactor`, `style`, `ci`, `build` and visible entries for `feat`, `fix`, `perf`, `revert`, `docs`, `deps`.

### `.release-please-manifest.json`

```json
{ ".": "0.1.0" }
```

First release cuts `v0.1.0`.

### `.goreleaser.yaml`

Sections:

- **`builds`** — single builder, `main: ./cmd/escrow-proxy`, `env: CGO_ENABLED=0`, `goos: [linux, darwin, windows]`, `goarch: [amd64, arm64]`, with `ignore: [{ goos: windows, goarch: arm64 }]` to stay at the 5 targets we committed to.
- **`ldflags`** — `-s -w -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}}`.
- **`archives`** — `tar.gz` for unix, `zip` for windows; name template `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}`.
- **`checksums`** — SHA256, output to `checksums.txt`.
- **`sboms`** — one SBOM per archive, generated by `syft`; attached as release assets.
- **`dockers`** — two entries (amd64, arm64), using `Dockerfile` from the repo root, tagged `ghcr.io/loopingz/escrow-proxy:{{.Version}}-amd64` / `-arm64`.
- **`docker_manifests`** — combines the per-arch images into `ghcr.io/loopingz/escrow-proxy:{{.Version}}` and `:latest`.
- **`signs`** — cosign keyless signing for `checksums.txt` and SBOMs (`COSIGN_EXPERIMENTAL=1`, OIDC via GitHub).
- **`docker_signs`** — cosign keyless signing for the Docker manifest.
- **`release`** — `github`, `draft: false`, `prerelease: auto` (so `v0.1.0-rc1` is auto-flagged).

### `Dockerfile`

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
COPY escrow-proxy /usr/local/bin/escrow-proxy
ENTRYPOINT ["/usr/local/bin/escrow-proxy"]
```

GoReleaser assembles the build context per-arch; the only thing it needs is the already-built binary. The distroless `static-debian12` image includes `/etc/ssl/certs/ca-certificates.crt`, which the proxy needs to validate upstream TLS.

### `cmd/escrow-proxy/main.go` addition

```go
var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)
```

A `--version` flag on the root Cobra command prints `escrow-proxy {version} ({commit}, {date})`. Defaults keep `go run` working without ldflags.

## Workflow details

### `release-please.yml`

```yaml
name: release-please
on:
  push:
    branches: [main]
permissions:
  contents: write
  pull-requests: write
jobs:
  release-please:
    runs-on: ubuntu-latest
    steps:
      - uses: googleapis/release-please-action@v4
        with:
          config-file: .release-please-config.json
          manifest-file: .release-please-manifest.json
```

Idempotent by design — runs on every push, updates the Release PR. No outputs consumed downstream.

### `release.yml`

```yaml
name: release
on:
  release:
    types: [published]
permissions:
  contents: write       # upload assets
  packages: write       # push to ghcr.io
  id-token: write       # cosign keyless OIDC
jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - uses: sigstore/cosign-installer@v3
      - uses: anchore/sbom-action/download-syft@v0
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: goreleaser/goreleaser-action@v6
        with:
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

## Error handling

- **Partial GoReleaser failure** (e.g., one arch's Docker push fails): the GitHub Release already exists; the workflow can be re-run from the Actions UI. `--clean` wipes `dist/` before each run so re-runs start from a clean slate.
- **Cosign OIDC exchange failure**: the release fails fast. Signing is mandatory in this design — a release without signatures is a regression on the supply-chain guarantee.
- **release-please-action retries**: safe. The action is idempotent; re-running on the same commit produces the same Release PR state.

## Testing plan

CI-workflow-in-CI testing is out of scope; all verification is manual.

1. **Local dry-run**: `goreleaser release --snapshot --clean --skip=publish,sign`
   - Builds all 5 binaries and the Docker images locally, no push, no signing.
   - Primary confidence check before merging the release automation.
2. **Config lint**: `goreleaser check`. For release-please: the action itself validates config on first run.
3. **Dockerfile smoke test**: after a snapshot build, `docker run --rm escrow-proxy:<snapshot-tag> --version` prints the injected version.
4. **First real release**: merge the initial release-please PR, watch `release.yml` in Actions, then verify:
   - 5 archives, `checksums.txt`, `checksums.txt.sig`, `checksums.txt.pem`, and per-archive SBOMs are attached to the release.
   - `ghcr.io/loopingz/escrow-proxy:0.1.0` and `:latest` are pullable for both amd64 and arm64.
   - `cosign verify ghcr.io/loopingz/escrow-proxy:0.1.0 --certificate-identity-regexp '.*' --certificate-oidc-issuer https://token.actions.githubusercontent.com` succeeds.

## Required repo settings (one-time)

- **Actions → General → Workflow permissions**: ensure "Read and write permissions" is enabled, or at minimum that the workflows' declared `permissions:` blocks are honored (default on new repos; confirm before the first release).
- **Packages**: first push to GHCR creates the package as private; switch visibility to public after the first release so `docker pull` works without auth.

## Permissions summary

| Workflow | Permission | Why |
|----------|------------|-----|
| `release-please.yml` | `contents: write` | Create tags and GitHub Releases. |
| `release-please.yml` | `pull-requests: write` | Open and update the Release PR. |
| `release.yml` | `contents: write` | Upload assets to the Release. |
| `release.yml` | `packages: write` | Push images to `ghcr.io`. |
| `release.yml` | `id-token: write` | Cosign keyless signing via GitHub OIDC. |
