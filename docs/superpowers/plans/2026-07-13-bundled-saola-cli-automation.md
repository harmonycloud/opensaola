# Bundled Saola CLI Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically build the architecture-matched `saola` binary into OpenSaola images, update the dev channel from CLI snapshots, and promote only stable CLI releases to master after fail-closed checks.

**Architecture:** `saola-cli` publishes immutable metadata and multi-architecture artifacts, then dispatches an authenticated update event. `opensaola` stores the exact CLI source revision in a validated lock file, builds it through a BuildKit named context with `TARGETOS/TARGETARCH`, and uses auditable auto-merge PRs for dev and master channels. Master never resolves `latest`; it consumes only a stable event carrying an immutable CLI commit.

**Tech Stack:** Go 1.26.4, GNU Make, Docker BuildKit/buildx, GitHub Actions, Bash, GitHub CLI, Cosign keyless signing, GitHub artifact attestations.

## Global Constraints

- Preserve all pre-existing uncommitted changes in both repositories.
- Do not push, tag, publish, configure secrets, or change external repositories during local implementation.
- Official image platforms are exactly `linux/amd64` and `linux/arm64`.
- CLI builds use `CGO_ENABLED=0`, `-trimpath`, and the CLI module's existing version ldflags.
- `dev` may consume CLI snapshots; `master` may consume only final stable `vMAJOR.MINOR.PATCH` tags.
- Every OpenSaola image records the exact CLI version and full 40-character commit.
- Automation fails closed on malformed versions, revisions, repositories, channels, missing checksums, failed tests, or absent credentials.
- Runtime remains UID/GID 65532 with `/app/manager` as the unchanged entrypoint and `/usr/local/bin/saola` mode `0555`.

---

### Task 1: Saola CLI release metadata and multi-architecture build

**Files:**
- Create: `saola-cli/hack/release-metadata.sh`
- Create: `saola-cli/hack/release-metadata_test.sh`
- Modify: `saola-cli/Makefile`
- Create: `saola-cli/.github/workflows/release.yml`
- Modify: `saola-cli/README.md`
- Modify: `saola-cli/README_zh.md`

**Interfaces:**
- Consumes: Git ref, full commit SHA, repository name, and optional `SOURCE_DATE_EPOCH`.
- Produces: `channel`, `version`, `commit`, `build_date`, two Linux binaries, `SHA256SUMS`, SBOM/provenance/signature artifacts, and `saola-cli-dev` or `saola-cli-stable` dispatch payloads.

- [ ] Write shell tests asserting `refs/heads/main` maps to `dev-<shortsha>`/`dev`, a SemVer tag maps to the tag/stable channel, malformed refs and non-40-character SHAs fail, and metadata is deterministic.
- [ ] Run `bash hack/release-metadata_test.sh` and confirm it fails because the metadata helper does not exist.
- [ ] Implement the smallest strict metadata helper that makes the tests pass without evaluating input as shell code.
- [ ] Add `release-build` and `release-checksums` Make targets that build only `linux/amd64` and `linux/arm64`, use the existing ldflags, and write deterministic files under `dist/`.
- [ ] Add a GitHub workflow that tests, builds, smoke-tests both architectures, creates checksums/SBOM/attestations/signatures, publishes tag assets, and dispatches immutable metadata to `harmonycloud/opensaola` using `OPENSAOLA_DISPATCH_TOKEN`.
- [ ] Document snapshot versus stable behavior and required repository secret.
- [ ] Run the helper tests, `make test`, `make release-build`, inspect both files with `file`, and run `saola version` for the native binary.

### Task 2: OpenSaola CLI lock contract and image integration

**Files:**
- Create: `opensaola/build/saola-cli-dev.lock`
- Create through stable dispatch on `dev`: `opensaola/build/saola-cli-stable-candidate.lock`
- Create only through promotion on `master`: `opensaola/build/saola-cli-stable.lock`
- Create: `opensaola/hack/saola-cli-lock.sh`
- Create: `opensaola/hack/saola-cli-lock_test.sh`
- Modify: `opensaola/Dockerfile`
- Modify: `opensaola/Makefile`
- Modify: `opensaola/.dockerignore`

**Interfaces:**
- Consumes: strict key/value lock fields `repository`, `version`, `commit`, `channel`, and `source_date_epoch`; BuildKit context named `saola-cli`.
- Produces: validated build arguments and `/usr/local/bin/saola` matching the final image platform.

- [ ] Write failing shell tests for a valid lock, duplicate/missing/unknown keys, invalid repository/channel/version/commit/epoch, and safe value extraction.
- [ ] Run `bash hack/saola-cli-lock_test.sh` and confirm it fails because the validator does not exist.
- [ ] Implement strict `validate` and `get` commands without sourcing the lock file.
- [ ] Add the initial lock using the clean committed `saola-cli` main revision, not the dirty working tree contents.
- [ ] Add a cached CLI builder stage using the named context and copy the static binary to `/usr/local/bin/saola` with mode `0555`.
- [ ] Update local `docker-build` and `docker-buildx` to validate/read the lock, pass the named context and build args, use exactly amd64/arm64, and remove obsolete `Dockerfile.cross` generation.
- [ ] Run lock tests, Dockerfile/Make dry-run checks, both CLI cross-builds, and a local single-platform image smoke if the container runtime is available.

### Task 3: Automated dev update and stable master promotion

**Files:**
- Create: `opensaola/.github/workflows/saola-cli-update.yml`
- Create: `opensaola/.github/workflows/saola-cli-promote.yml`
- Modify: `opensaola/.github/workflows/docker.yml`
- Modify: `opensaola/docs/release-process.md`
- Modify: `opensaola/docs/release-process_zh.md`

**Interfaces:**
- Consumes: authenticated `repository_dispatch` payload produced by Task 1 and the validated lock contract from Task 2.
- Produces: auto-merge update PRs targeting `dev` for snapshots/stable releases and targeting `master` only for stable releases after required checks and configured soak policy.

- [ ] Add static workflow contract tests to the lock test script for event names, permissions, exact branch/channel routing, auto-merge, concurrency, and fail-closed secret checks; run them and observe failure before creating workflows.
- [ ] Implement update workflow payload validation, source revision verification, lock update, auditable PR creation, and auto-merge.
- [ ] Implement stable promotion workflow that rejects snapshots, verifies the stable lock and successful checks for the exact candidate, enforces the configured soak interval, updates master through an auto-merge PR, and never directly pushes master.
- [ ] Pass lock-derived CLI context and version args into the existing multi-architecture Docker workflow and add OCI labels/attestations.
- [ ] Document dev/master/tag behavior, required branch protection, secrets, environments, rollback/denylist expectations, and the fact that external setup remains necessary before workflows can operate.
- [ ] Run workflow contract tests and available workflow syntax validation.

### Task 4: Cross-repository verification and review

**Files:**
- Review all files changed by Tasks 1-3.

**Interfaces:**
- Consumes: both repository diffs and all test reports.
- Produces: verified implementation status, explicit external prerequisites, and no unverified completion claims.

- [ ] Confirm unrelated pre-existing `saola-cli` modifications remain intact and outside this feature diff.
- [ ] Run `git diff --check` in both repositories.
- [ ] Run all new shell contract tests and both Go unit suites.
- [ ] Cross-compile CLI for both official architectures and inspect static ELF architecture.
- [ ] Validate Docker/Make/workflow wiring with available local tools; record any check that cannot run locally rather than claiming it passed.
- [ ] Conduct task-scoped and whole-change reviews, fix all Critical/Important findings, and rerun covering tests.
- [ ] Report changed files, verified evidence, unconfigured GitHub secrets/branch rules/environments, and the exact next external activation steps.
