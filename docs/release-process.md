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

## Local Helm Deploy

Use the Makefile wrapper from a source checkout:

```bash
make helm-deploy
```

The wrapper uses an exact `v*` tag when the current commit is a release tag. Otherwise, when the current branch is `dev`, `master`, or `main`, it deploys the current commit image tag (`sha-<shortsha>`). Short-lived branches fall back to `dev` because their SHA images are not pushed by default.

For `sha-*` deployments, wait until the Docker workflow for that commit has completed so the image exists in GHCR.

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
