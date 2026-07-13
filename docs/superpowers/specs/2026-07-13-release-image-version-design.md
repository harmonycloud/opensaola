# OpenSaola Image Build Version Design

## Goal

Make the OpenSaola manager binary, Docker build arguments, and OCI image metadata describe the same immutable source revision. A release image built from `vX.Y.Z` must expose that tag, the full Git commit, and a deterministic commit timestamp through `manager --version`.

This design does not change Helm Chart version injection. The published `opensaola:1.0.0` OCI Chart already contains `version: 1.0.0`, `appVersion: v1.0.0`, and the matching image reference.

## Current behavior

- `Dockerfile` declares `VERSION=dev`, `GIT_COMMIT=unknown`, and `BUILD_DATE=unknown` and passes them to linker flags.
- The referenced `main.version`, `main.gitCommit`, and `main.buildDate` variables do not exist, so the manager has no reliable observable build information.
- The Docker workflow does not pass those build arguments.
- `docker/metadata-action` currently overrides OCI labels correctly for hosted release builds, but the manager binary and local Docker builds remain disconnected from those labels.
- The metadata action uses workflow execution time for `created`; a rebuild of the same commit therefore does not have deterministic build metadata.

## Approaches considered

### 1. Keep metadata only in OCI labels

This preserves the existing hosted image labels but leaves the running process unable to identify itself. Diagnostics inside a Pod would still depend on registry access. Rejected as incomplete.

### 2. Declare build variables directly in `package main`

This is the smallest code change and makes existing `-X main.*` flags usable. It couples reusable build information to the manager entrypoint and makes unit testing/formatting awkward. Rejected in favor of a small package boundary.

### 3. Add an `internal/version` package and a `--version` flag

Selected. The package owns default values and stable text formatting. The manager prints them and exits before reading Kubernetes configuration. Docker and local Make builds inject the package variables through full linker symbol paths.

## Design

### Version API

Add `internal/version` with three link-time string variables:

```text
Version   = dev
GitCommit = unknown
BuildDate = unknown
```

It exposes deterministic human-readable formatting:

```text
Version: v1.0.1
Git Commit: <40-character SHA>
Build Date: <RFC3339 commit timestamp>
```

Defaults remain valid for ordinary local `go run` and unit-test builds.

### Manager behavior

Add the standard boolean flag `--version`. After `flag.Parse`, the manager prints version information to stdout and returns before loading configuration, creating Kubernetes clients, or starting controllers.

Normal startup logs the same three values once, allowing Pod logs to identify the running build without registry access.

### Build metadata contract

The Docker workflow resolves one metadata tuple per run:

```text
version    = vX.Y.Z for a release tag, the branch name for branch builds, or pr-N for pull requests
commit     = github.sha as a full 40-character SHA
build_date = the checked-out commit's RFC3339 committer timestamp
```

The workflow passes this tuple as `VERSION`, `GIT_COMMIT`, and `BUILD_DATE` build arguments. The Dockerfile injects the tuple into `internal/version` using full Go import paths and keeps the same values as fallback OCI labels for local builds. Hosted builds continue using metadata-action labels but explicitly override version, revision, and created with this tuple so both paths agree, including the leading `v` on stable releases.

Makefile Docker targets pass locally resolved values by default and allow explicit release values. Build time is derived from the commit rather than the current clock.

### Release semantics

- A stable `vX.Y.Z` tag produces manager `Version: vX.Y.Z`.
- `revision` and manager `Git Commit` equal the exact tag commit.
- Rebuilding the same commit and version keeps the same build date.
- Development builds remain visibly non-stable and never invent a SemVer release value.

## Testing

Follow red-green TDD with these contracts:

1. Unit tests for default and injected version formatting.
2. A build contract test that compiles the manager with explicit linker values and asserts `manager --version` output and zero exit status without a cluster.
3. Static workflow/Docker contracts requiring full linker paths and all three build arguments.
4. `go test` for the new package and the repository unit-test target.
5. `actionlint` and `git diff --check`.
6. A real local Docker build followed by `docker run --entrypoint /app/manager ... --version` and `docker image inspect` when the container runtime is available.

## Failure behavior

- Missing explicit metadata in an ordinary local build falls back to `dev/unknown/unknown`.
- Release workflow metadata must be non-empty; tag releases must match strict `vMAJOR.MINOR.PATCH` or prerelease syntax.
- The release workflow fails before publishing if binary output, build arguments, or expected version identity disagree.

## Scope boundaries

- No changes to Chart version injection or the Helm publishing workflow.
- No changes to `saola-cli` versioning in this task.
- No changes to controller behavior beyond the early `--version` exit and one startup metadata log.
- Existing owner-reference and synchronizer work remains untouched.
