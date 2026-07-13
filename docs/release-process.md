# Release Process

> Chinese version: [release-process_zh.md](release-process_zh.md)

This project uses a GitHub Flow style workflow with lightweight release branches when older minor versions need patch support. Release artifacts are driven by immutable `v*` Git tags.

## Branch Policy

| Branch | Purpose | Rules |
|--------|---------|-------|
| `master` | Stable trunk and default integration branch | Protected, PR-only, CI must pass |
| `dev` | Optional unstable integration branch | May exist for test deployments, then merge forward to `master` |
| `feature/*`, `fix/*`, `docs/*` | Short-lived work branches | Open PRs into `dev` or `master`, delete after merge |
| `release/x.y` | Patch support for an older minor release | Only accepts backports and hotfixes |
| `hotfix/*` | Temporary emergency fix branch | Merge into trunk first, then backport to `release/x.y` when needed |

Do not keep branch-specific values in source files. In particular, `chart/opensaola/Chart.yaml` must not be edited differently on `dev` and `master`; the release workflow injects the published chart version and app version from the Git tag.

## Helm Chart Policy

`Chart.yaml.version` is the Helm chart package version. It must be SemVer and identifies the chart package.

`Chart.yaml.appVersion` is application metadata and is also used as the default image tag when `image.tag` is empty.

Source branches keep development-safe defaults:

```yaml
appVersion: "dev"
version: 0.1.3-dev.0
```

Official releases are generated from tags. For `v0.1.3`, the Helm release workflow packages:

```bash
helm package chart/opensaola \
  --version "0.1.3" \
  --app-version "v0.1.3"
```

This keeps `dev` and `master` merge-friendly while still producing release charts with exact versions.

If a chart-only fix is needed, bump only the chart version. The application image version can remain unchanged.

## Image Tag Policy

| Image tag | Meaning | Use |
|-----------|---------|-----|
| `dev`, `master`, `main` | Floating branch images | Development and integration tests |
| `pr-<number>` | Pull request preview tag | Build validation only; not pushed by default |
| `sha-<shortsha>` | Commit tracking tag | Debugging and rollback |
| `v0.1.3`, `0.1.3`, `0.1` | Release tags | Stable installs |
| `latest` | Latest stable release | Produced only from stable `v*` tags |

The Helm chart default `image.pullPolicy` is `IfNotPresent` for reproducible release installs. The Makefile also uses `IfNotPresent` for immutable `sha-*` and `v*` tags, and switches to `Always` only when a floating tag such as `dev`, `master`, `main`, or `latest` is explicitly selected.

## Bundled Saola CLI Resolution

Saola CLI publishes independently. OpenSaola does not require a downstream dispatch token, automation PAT, bot login, label, scheduled promotion, candidate lock, or soak window. The Docker workflow resolves the highest published final SemVer Saola CLI Release at image-build time, verifies its tag, assets, checksums, and reproducible rebuilds, then pins the result in a strict five-field lock.

`build/saola-cli-stable.lock` is the only committed Saola CLI lock. It records `repository`, `version`, `commit`, `channel`, and `source_date_epoch`. Docker builds use a BuildKit named context pinned to that full commit and record the CLI version and revision in OCI labels. Official images remain exactly `linux/amd64` and `linux/arm64` and include BuildKit provenance and SBOM attestations.

Branch and tag behavior is intentionally different:

| Git ref | Behavior |
|---------|----------|
| `dev` | Resolves the latest eligible final Saola CLI Release. If the committed stable lock is missing or stale, a dedicated sync job commits only `build/saola-cli-stable.lock` to `dev` with the repository `GITHUB_TOKEN`, explicitly re-runs `docker.yml` on the updated `dev`, and the current run does not publish. |
| `master` | Resolves the latest eligible final Release, but never writes. If the committed stable lock is missing or stale, the workflow fails and `dev` must sync the lock before merging forward. |
| `v*` tags | Do not resolve a newer CLI. The workflow verifies the stable lock already present in the tagged commit and builds from that immutable dependency record. |
| Pull requests | Build from the committed stable lock when it exists. Before the first final Saola CLI Release, missing-lock PRs perform an explicit bootstrap no-op instead of publishing. |

The bootstrap is fail-closed: until the first final Saola CLI Release exists and `dev` has synchronized `build/saola-cli-stable.lock`, `master` cannot publish an image and release tags fail instead of falling back to a floating CLI revision.

### Required GitHub configuration

No cross-repository secret is required. The only write path is the isolated `dev` lock sync job using the repository `GITHUB_TOKEN`, with `contents: write` and `actions: write`, so it can commit the stable lock and explicitly dispatch the follow-up Docker run.

Repository administrators should still verify the normal platform controls before relying on releases:

- Protect `master` as PR-only and require the relevant CI, Docker, and Helm checks.
- Allow the Docker workflow on `dev` to push the single stable-lock commit, or replace that write path with an equivalent GitHub App if `dev` is protected against workflow pushes.
- Keep release tags immutable and make sure formal releases are cut only after `master` is green with a current stable lock.
- Use normal reviewed lock changes for rollback to a previously verified Saola CLI version; never edit the lock to a floating ref.

Local validation can prove the workflow contract and lock contents, but it cannot prove hosted-runner permissions or branch protection behavior. Verify those settings in GitHub before relying on the path.

## Local Helm Deploy

Use the Makefile wrapper from a source checkout:

```bash
make helm-deploy
```

The wrapper uses an exact `v*` tag when the current commit is a release tag. Otherwise, when the current branch is `dev`, `master`, or `main`, it deploys the current commit image tag (`sha-<shortsha>`). Short-lived branches fall back to `dev` because their SHA images are not pushed by default.

For `sha-*` deployments, wait until the Docker workflow for that commit has completed so the image exists in GHCR.

On a server that tracks `dev`, the exact-commit upgrade command is:

```bash
git pull --ff-only && make helm-deploy
```

To follow the floating `dev` tag instead, use the explicit rollout target:

```bash
make helm-deploy-dev
```

The floating-tag target sets `image.tag=dev`, `image.pullPolicy=Always`, and refreshes `podAnnotations.redeployAt` so the Deployment restarts even though the tag string stays the same.

Override the image tag for release or SHA testing:

```bash
HELM_IMAGE_TAG=v0.1.3 make helm-deploy
HELM_IMAGE_TAG=sha-abcdef1 make helm-deploy
```

## Release Checklist

1. Merge all intended changes into `master`.
2. Ensure CI and Docker workflows are green on `master`.
3. Create an immutable release tag:

   ```bash
   git tag v0.1.3
   git push origin v0.1.3
   ```

4. Confirm the Docker workflow published the matching image tags, especially `vX.Y.Z` and `sha-<shortsha>`.
5. Confirm the Helm Chart Release workflow published the matching OCI chart. The workflow injects `--version X.Y.Z` and `--app-version vX.Y.Z`; do not hand-edit those values differently on `master`.
6. Create or update the GitHub Release notes with image and chart references.
7. Bump source branch development metadata only when the next development line changes; keep the same `Chart.yaml` content across `dev` and `master`.

## Conflict Resolution Rule

When merging `dev` into `master`, prefer the version of files that preserves branch-neutral release automation. Do not resolve conflicts by setting `appVersion` to `master` on one branch and `dev` on another. Branch-specific image selection belongs in CI or Makefile values, not in chart source metadata.
