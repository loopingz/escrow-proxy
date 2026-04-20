# Release Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire release-please + GoReleaser into GitHub Actions so a merge to `main` eventually produces a signed, multi-arch GitHub Release with binaries and a container image on `ghcr.io/loopingz/escrow-proxy`.

**Architecture:** Two decoupled GitHub Actions workflows. `release-please.yml` maintains the Release PR on every push to `main` and cuts a tag + Release when that PR merges. The `release: published` event then triggers `release.yml`, which runs GoReleaser to build 5 binaries, multi-arch Docker images, checksums, SBOMs, and cosign-keyless signatures, and uploads everything to the Release and GHCR.

**Tech Stack:** Go 1.25, Cobra CLI, GitHub Actions, release-please (googleapis/release-please-action v4), GoReleaser v6, syft (SBOM), cosign keyless via GitHub OIDC, distroless `static-debian12:nonroot` base image.

**Spec:** `docs/superpowers/specs/2026-04-19-release-automation-design.md`

---

## File Structure

| File | Responsibility |
|------|----------------|
| `cmd/escrow-proxy/main.go` (mod) | Adds `version/commit/date` package vars + a version template on the root Cobra command so `--version` prints build metadata. |
| `cmd/escrow-proxy/version.go` (new) | Isolates the `buildVersion(v, c, d string) string` helper so it can be unit-tested without exercising Cobra. |
| `cmd/escrow-proxy/version_test.go` (new) | Unit test for `buildVersion`. |
| `Dockerfile` (new) | Minimal distroless image layered over the pre-built binary GoReleaser injects. |
| `.dockerignore` (new) | Trims build context (`.git`, `dist/`, tests). |
| `.goreleaser.yaml` (new) | Full GoReleaser config: builds, archives, checksums, SBOMs, dockers, docker_manifests, signs, docker_signs, release. |
| `.release-please-config.json` (new) | release-please config: Go release type, conventional-commit sections. |
| `.release-please-manifest.json` (new) | Current version tracker — starts at `0.1.0`. |
| `.github/workflows/release-please.yml` (new) | Runs release-please-action on pushes to `main`. |
| `.github/workflows/release.yml` (new) | Runs GoReleaser on `release: published`. |
| `.gitignore` (mod) | Adds `dist/` (GoReleaser output). |
| `README.md` (mod) | Adds container pull + `cosign verify` snippets. |

Each file has a single responsibility; workflow files do not read each other's outputs.

---

## Task 1: Isolate a testable build-version helper

**Files:**
- Create: `cmd/escrow-proxy/version.go`
- Test: `cmd/escrow-proxy/version_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/escrow-proxy/version_test.go`:

```go
package main

import "testing"

func TestBuildVersion(t *testing.T) {
	tests := []struct {
		name           string
		version        string
		commit         string
		date           string
		want           string
	}{
		{
			name:    "populated",
			version: "1.2.3",
			commit:  "abcdef1",
			date:    "2026-04-19T10:00:00Z",
			want:    "escrow-proxy 1.2.3 (commit abcdef1, built 2026-04-19T10:00:00Z)",
		},
		{
			name:    "defaults",
			version: "dev",
			commit:  "none",
			date:    "unknown",
			want:    "escrow-proxy dev (commit none, built unknown)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildVersion(tc.version, tc.commit, tc.date)
			if got != tc.want {
				t.Fatalf("buildVersion(%q, %q, %q) = %q, want %q",
					tc.version, tc.commit, tc.date, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/escrow-proxy/ -run TestBuildVersion`
Expected: FAIL with `undefined: buildVersion`.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/escrow-proxy/version.go`:

```go
package main

import "fmt"

// buildVersion renders the --version output. The three inputs are populated at
// build time by GoReleaser's ldflags (see .goreleaser.yaml) and left at their
// "dev"/"none"/"unknown" defaults for `go run` / ad-hoc builds.
func buildVersion(version, commit, date string) string {
	return fmt.Sprintf("escrow-proxy %s (commit %s, built %s)", version, commit, date)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/escrow-proxy/ -run TestBuildVersion -v`
Expected: PASS — both subtests green.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-proxy/version.go cmd/escrow-proxy/version_test.go
git commit -m "feat(cli): add buildVersion helper for --version output"
```

---

## Task 2: Wire version metadata into the root Cobra command

**Files:**
- Modify: `cmd/escrow-proxy/main.go`

- [ ] **Step 1: Add package-level version vars**

At the top of `cmd/escrow-proxy/main.go`, immediately after the `import` block (after line 22), add:

```go
// Build-time metadata populated via ldflags by GoReleaser. Defaults keep
// `go run` and local `go build` usable without any flags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)
```

- [ ] **Step 2: Attach the version to the root command**

In `main()` in `cmd/escrow-proxy/main.go`, replace these lines:

```go
	rootCmd := &cobra.Command{
		Use:   "escrow-proxy",
		Short: "MITM caching proxy for CI/CD dependency caching",
	}
```

with:

```go
	rootCmd := &cobra.Command{
		Use:     "escrow-proxy",
		Short:   "MITM caching proxy for CI/CD dependency caching",
		Version: buildVersion(version, commit, date),
	}
	rootCmd.SetVersionTemplate("{{.Version}}\n")
```

Setting `.Version` makes Cobra auto-register a `--version` / `-v` flag. The custom template prints exactly what `buildVersion` returned without Cobra's default `{name} version {version}` wrapping.

- [ ] **Step 3: Build and smoke-test the flag**

Run:

```bash
go build -o /tmp/escrow-proxy ./cmd/escrow-proxy
/tmp/escrow-proxy --version
```

Expected output:

```
escrow-proxy dev (commit none, built unknown)
```

Also verify nothing else broke:

```bash
/tmp/escrow-proxy --help
```

Expected: help text prints, lists `serve`, `record`, `offline`, `ca` subcommands.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-proxy/main.go
git commit -m "feat(cli): expose --version with build metadata"
```

---

## Task 3: Ignore the GoReleaser output directory

**Files:**
- Modify: `.gitignore`

- [ ] **Step 1: Add `dist/` to `.gitignore`**

Current `.gitignore`:

```
.worktrees/
/escrow-proxy
```

Replace with:

```
.worktrees/
/escrow-proxy
dist/
```

- [ ] **Step 2: Verify**

Run: `cat .gitignore`
Expected: three lines, the last being `dist/`.

- [ ] **Step 3: Commit**

```bash
git add .gitignore
git commit -m "chore: ignore GoReleaser dist/ output"
```

---

## Task 4: Add the Dockerfile and .dockerignore

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`

- [ ] **Step 1: Create `.dockerignore`**

```
.git
.github
.worktrees
dist
docs
*.md
*_test.go
```

- [ ] **Step 2: Create `Dockerfile`**

```dockerfile
# syntax=docker/dockerfile:1

# GoReleaser places the already-built binary in the build context as
# ./escrow-proxy for each target arch; this Dockerfile just wraps it.
FROM gcr.io/distroless/static-debian12:nonroot

COPY escrow-proxy /usr/local/bin/escrow-proxy

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/escrow-proxy"]
```

`gcr.io/distroless/static-debian12:nonroot` ships `/etc/ssl/certs/ca-certificates.crt`, which the proxy needs to validate upstream TLS, and runs as UID 65532 — no shell, no package manager.

- [ ] **Step 3: Smoke-test the Dockerfile locally**

Build a linux/amd64 binary and stage it where the Dockerfile expects, then build and run:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ./escrow-proxy ./cmd/escrow-proxy
docker build -t escrow-proxy:smoke .
docker run --rm escrow-proxy:smoke --version
rm ./escrow-proxy
```

Expected:
- `docker build` succeeds.
- `docker run` prints `escrow-proxy dev (commit none, built unknown)` and exits 0.

If Docker isn't available locally, skip Step 3 — the same check runs inside GoReleaser and will surface in the first release.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile .dockerignore
git commit -m "feat: add distroless Dockerfile for release images"
```

---

## Task 5: Add the GoReleaser configuration

**Files:**
- Create: `.goreleaser.yaml`

- [ ] **Step 1: Create `.goreleaser.yaml`**

```yaml
version: 2

project_name: escrow-proxy

before:
  hooks:
    - go mod tidy

builds:
  - id: escrow-proxy
    main: ./cmd/escrow-proxy
    binary: escrow-proxy
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64
    ignore:
      - goos: windows
        goarch: arm64
    flags:
      - -trimpath
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.Date}}

archives:
  - id: default
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files:
      - README.md
      - LICENSE*

checksum:
  name_template: "checksums.txt"
  algorithm: sha256

sboms:
  - id: archive-sbom
    artifacts: archive

dockers:
  - id: amd64
    use: buildx
    goos: linux
    goarch: amd64
    image_templates:
      - "ghcr.io/loopingz/escrow-proxy:{{ .Version }}-amd64"
    build_flag_templates:
      - "--platform=linux/amd64"
      - "--label=org.opencontainers.image.title={{ .ProjectName }}"
      - "--label=org.opencontainers.image.version={{ .Version }}"
      - "--label=org.opencontainers.image.revision={{ .FullCommit }}"
      - "--label=org.opencontainers.image.source=https://github.com/loopingz/escrow-proxy"
      - "--label=org.opencontainers.image.licenses=Apache-2.0"

  - id: arm64
    use: buildx
    goos: linux
    goarch: arm64
    image_templates:
      - "ghcr.io/loopingz/escrow-proxy:{{ .Version }}-arm64"
    build_flag_templates:
      - "--platform=linux/arm64"
      - "--label=org.opencontainers.image.title={{ .ProjectName }}"
      - "--label=org.opencontainers.image.version={{ .Version }}"
      - "--label=org.opencontainers.image.revision={{ .FullCommit }}"
      - "--label=org.opencontainers.image.source=https://github.com/loopingz/escrow-proxy"
      - "--label=org.opencontainers.image.licenses=Apache-2.0"

docker_manifests:
  - name_template: "ghcr.io/loopingz/escrow-proxy:{{ .Version }}"
    image_templates:
      - "ghcr.io/loopingz/escrow-proxy:{{ .Version }}-amd64"
      - "ghcr.io/loopingz/escrow-proxy:{{ .Version }}-arm64"
  - name_template: "ghcr.io/loopingz/escrow-proxy:latest"
    image_templates:
      - "ghcr.io/loopingz/escrow-proxy:{{ .Version }}-amd64"
      - "ghcr.io/loopingz/escrow-proxy:{{ .Version }}-arm64"

signs:
  - cmd: cosign
    signature: "${artifact}.sig"
    certificate: "${artifact}.pem"
    args:
      - "sign-blob"
      - "--yes"
      - "--output-signature=${signature}"
      - "--output-certificate=${certificate}"
      - "${artifact}"
    artifacts: checksum
    output: true

docker_signs:
  - cmd: cosign
    args:
      - "sign"
      - "--yes"
      - "${artifact}@${digest}"
    artifacts: manifests
    output: true

release:
  github:
    owner: loopingz
    name: escrow-proxy
  draft: false
  prerelease: auto

changelog:
  disable: true
```

Notes:
- `changelog: disable: true` — release-please already writes the changelog on the Release body; letting GoReleaser overwrite it would duplicate/clobber.
- `license: Apache-2.0` in the OCI label is a placeholder aligned with a common default — if the repo adds a different `LICENSE` later, update this string.
- `sign-blob` + keyless (no `--key`) uses GitHub OIDC; requires `id-token: write` and `--yes` to skip the interactive prompt.

- [ ] **Step 2: Validate the config**

Install GoReleaser locally (or use Docker):

```bash
# Option A: brew (macOS)
brew install goreleaser/tap/goreleaser

# Option B: go install
go install github.com/goreleaser/goreleaser/v2@latest
```

Then:

```bash
goreleaser check
```

Expected: `checks passed`. If it complains about a field, fix it inline — the config is the source of truth.

- [ ] **Step 3: Dry-run a snapshot build**

```bash
goreleaser release --snapshot --clean --skip=publish,sign
```

Expected:
- Builds 5 binaries under `dist/` (directories like `escrow-proxy_linux_amd64_v1/escrow-proxy`).
- Builds 2 Docker images locally (`ghcr.io/loopingz/escrow-proxy:<snapshot-version>-amd64` and `-arm64`). This step requires a running Docker daemon with buildx; if Docker isn't available locally, add `--skip=docker` and verify only the binary targets.
- No network calls to GHCR, no signing.

Inspect one of the binaries:

```bash
./dist/escrow-proxy_linux_amd64_v1/escrow-proxy --version
```

Expected: prints something like `escrow-proxy 0.0.0-next (commit <sha>, built <date>)` — confirms ldflags are wired through.

- [ ] **Step 4: Commit**

```bash
git add .goreleaser.yaml
git commit -m "feat: add GoReleaser config for multi-arch release builds"
```

---

## Task 6: Add release-please configuration

**Files:**
- Create: `.release-please-config.json`
- Create: `.release-please-manifest.json`

- [ ] **Step 1: Create `.release-please-manifest.json`**

```json
{
  ".": "0.1.0"
}
```

- [ ] **Step 2: Create `.release-please-config.json`**

```json
{
  "$schema": "https://raw.githubusercontent.com/googleapis/release-please/main/schemas/config.json",
  "release-type": "go",
  "include-component-in-tag": false,
  "include-v-in-tag": true,
  "bump-minor-pre-major": true,
  "bump-patch-for-minor-pre-major": false,
  "packages": {
    ".": {
      "package-name": "escrow-proxy",
      "changelog-sections": [
        { "type": "feat", "section": "Features" },
        { "type": "fix", "section": "Bug Fixes" },
        { "type": "perf", "section": "Performance" },
        { "type": "revert", "section": "Reverts" },
        { "type": "docs", "section": "Documentation" },
        { "type": "deps", "section": "Dependencies" },
        { "type": "chore", "hidden": true },
        { "type": "test", "hidden": true },
        { "type": "refactor", "hidden": true },
        { "type": "style", "hidden": true },
        { "type": "ci", "hidden": true },
        { "type": "build", "hidden": true }
      ]
    }
  }
}
```

- [ ] **Step 3: Validate JSON**

Run:

```bash
python3 -c 'import json; json.load(open(".release-please-config.json")); json.load(open(".release-please-manifest.json")); print("ok")'
```

Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
git add .release-please-config.json .release-please-manifest.json
git commit -m "feat: add release-please config for conventional-commit releases"
```

---

## Task 7: Add the release-please workflow

**Files:**
- Create: `.github/workflows/release-please.yml`

- [ ] **Step 1: Create the workflow**

```yaml
name: release-please

on:
  push:
    branches:
      - main

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

- [ ] **Step 2: Lint YAML**

Run:

```bash
python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/release-please.yml")); print("ok")'
```

Expected: `ok`.

(If `actionlint` is available locally, `actionlint .github/workflows/release-please.yml` catches more issues.)

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release-please.yml
git commit -m "ci: add release-please workflow"
```

---

## Task 8: Add the release (GoReleaser) workflow

**Files:**
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Create the workflow**

```yaml
name: release

on:
  release:
    types: [published]

permissions:
  contents: write
  packages: write
  id-token: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: Install cosign
        uses: sigstore/cosign-installer@v3

      - name: Install syft
        uses: anchore/sbom-action/download-syft@v0

      - name: Setup QEMU
        uses: docker/setup-qemu-action@v3

      - name: Setup Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 2: Lint YAML**

Run:

```bash
python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/release.yml")); print("ok")'
```

Expected: `ok`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add GoReleaser workflow triggered by published releases"
```

---

## Task 9: Document the release artifacts in the README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add container pull + verify snippets**

Open `README.md`. Find the `## Installation` section (it currently shows `go install ...` and `go build ...`). Add a third subsection immediately after the `go build` block and before `## Quick Start`.

Replace:

```markdown
Or build from source:

```bash
git clone https://github.com/loopingz/escrow-proxy.git
cd escrow-proxy
go build -o escrow-proxy ./cmd/escrow-proxy
```

## Quick Start
```

with:

```markdown
Or build from source:

```bash
git clone https://github.com/loopingz/escrow-proxy.git
cd escrow-proxy
go build -o escrow-proxy ./cmd/escrow-proxy
```

### Container image

Multi-arch (`linux/amd64`, `linux/arm64`) images are published to GHCR on every release:

```bash
docker pull ghcr.io/loopingz/escrow-proxy:latest
# or pin to a version
docker pull ghcr.io/loopingz/escrow-proxy:0.1.0
```

### Verifying release artifacts

Release binaries and container images are signed with [cosign](https://docs.sigstore.dev/) using keyless OIDC signing via GitHub Actions.

Verify a container image:

```bash
cosign verify \
  --certificate-identity-regexp "^https://github.com/loopingz/escrow-proxy/.github/workflows/release.yml@.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/loopingz/escrow-proxy:0.1.0
```

Verify the checksum file for a binary release:

```bash
# Download from https://github.com/loopingz/escrow-proxy/releases
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp "^https://github.com/loopingz/escrow-proxy/.github/workflows/release.yml@.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# Then check the binary's SHA against checksums.txt
sha256sum -c checksums.txt --ignore-missing
```

## Quick Start
```

- [ ] **Step 2: Verify the edit**

Run:

```bash
grep -n "Container image" README.md
grep -n "cosign verify" README.md
```

Expected: both greps return at least one match with sensible line numbers inside the Installation section (before Quick Start).

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document container image and cosign verification"
```

---

## Task 10: Final verification

**Files:** none — this is a review/validation task.

- [ ] **Step 1: Full test suite still passes**

Run: `go test ./...`
Expected: all tests pass, including `TestBuildVersion`.

- [ ] **Step 2: GoReleaser config final check**

Run: `goreleaser check`
Expected: `checks passed`.

- [ ] **Step 3: Full snapshot build**

Run:

```bash
rm -rf dist/
goreleaser release --snapshot --clean --skip=publish,sign
```

Expected, under `dist/`:
- 5 archive files matching `escrow-proxy_*_*_*.tar.gz` / `.zip`.
- `checksums.txt`.
- 5 SBOM files (one per archive, named `*.sbom.json` or similar).
- 2 Docker images built locally (verify with `docker images | grep escrow-proxy`).

If Docker isn't available on the dev machine, rerun with `--skip=docker` to confirm the non-Docker pipeline.

- [ ] **Step 4: Confirm all created files are tracked**

Run: `git status`
Expected: `nothing to commit, working tree clean`. If anything is untracked, it means a step earlier skipped a commit — fix before proceeding.

- [ ] **Step 5: Confirm the commit list looks right**

Run: `git log --oneline -n 10`
Expected (newest first):

```
<sha> docs: document container image and cosign verification
<sha> ci: add GoReleaser workflow triggered by published releases
<sha> ci: add release-please workflow
<sha> feat: add release-please config for conventional-commit releases
<sha> feat: add GoReleaser config for multi-arch release builds
<sha> feat: add distroless Dockerfile for release images
<sha> chore: ignore GoReleaser dist/ output
<sha> feat(cli): expose --version with build metadata
<sha> feat(cli): add buildVersion helper for --version output
<sha> docs: add release automation design spec
```

Ten distinct commits, each self-contained.

---

## Post-merge operator checklist (not automatable)

Things the human maintainer needs to do once, in GitHub's UI, before the first release can succeed:

1. **Settings → Actions → General → Workflow permissions**: ensure "Read and write permissions" is enabled, and "Allow GitHub Actions to create and approve pull requests" is checked (release-please needs this to open its Release PR).
2. **First tag lifecycle**: the very first `release-please` run will open PR `chore: release 0.1.0`. Merge it; release-please then tags `v0.1.0` and creates the GitHub Release, which fires `release.yml`.
3. **GHCR visibility**: the first image push creates the package as private. Navigate to `https://github.com/loopingz/escrow-proxy/pkgs/container/escrow-proxy` → Package settings → Change visibility → Public, so anonymous `docker pull` works.
