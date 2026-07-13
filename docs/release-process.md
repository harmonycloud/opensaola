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

## Bundled Saola CLI Channels

`build/saola-cli-dev.lock` is the dev image input, `build/saola-cli-stable-candidate.lock` is a soaked candidate that may exist on `dev`, and `build/saola-cli-stable.lock` is the only CLI input accepted by `master` and release tags. A normal `dev -> master` merge may carry the candidate file but cannot change which CLI a master image selects. Only the promotion workflow copies the exact candidate into the promoted stable lock. The Docker workflow builds from a BuildKit named context pinned to the selected full commit and records the CLI version and revision in OCI labels. Official images remain exactly `linux/amd64` and `linux/arm64` and include BuildKit provenance and SBOM attestations.

The `saola-cli` repository may dispatch either of these immutable events:

| Event | Accepted lock | Destination |
|-------|---------------|-------------|
| `saola-cli-dev` | `channel=dev`, `version=dev-<12-character commit prefix>` | Auto-merge PR into `dev` |
| `saola-cli-stable` | `channel=stable`, final `vMAJOR.MINOR.PATCH` tag | Update the candidate lock on `dev`, labelled `automation:saola-cli-stable` |

Both automatic and manual dispatches must supply `repository`, `channel`, `version`, the full 40-character `commit`, `source_date_epoch`, and lowercase 64-character SHA-256 checksums for both Linux artifacts. The update workflow binds the epoch to the commit timestamp, rebuilds both artifacts, and for stable releases also compares the payload with the published `SHA256SUMS` asset before writing the strict five-field lock. It never pushes directly to `dev` or `master`.

Stable automation accepts only a same-name, non-draft, non-prerelease GitHub Release that is currently the latest published final release by `published_at`; replaying an older valid tag fails closed. The hourly promotion workflow selects the newest merged stable-labelled update PR, reads `build/saola-cli-stable-candidate.lock` from that PR's exact merge commit (not the later `dev` head), and waits 24 hours by default from `mergedAt`. It requires successful `CI`, the Docker workflow, and the concrete `Build stable candidate` check for that exact merge SHA. It exits without a PR only when `master` already has the identical complete stable lock; otherwise it opens a PR that writes only `build/saola-cli-stable.lock`. Master PR checks reject any stable-lock change not authored by the configured automation login from a deterministic promotion branch. Promotion never resolves `latest`, a snapshot, a prerelease, or another floating CLI revision.

The initial bootstrap is explicit and fail-closed: the workflow files may first enter the default branch while no final Saola CLI release exists, but master pushes publish no image and release tags fail until a promoted stable lock exists. After the first final Saola CLI Release, the normal candidate, exact-build, soak, and promotion path creates that lock without manual file edits.

### Required GitHub configuration

These files do not activate the automation by themselves. Repository administrators must configure all of the following externally:

- Bootstrap both `saola-cli-update.yml` and `saola-cli-promote.yml` on the repository's default branch, `master`, before sending a `repository_dispatch` or expecting a scheduled run. GitHub only routes these event types to workflows present on the default branch. During this bootstrap window, missing `build/saola-cli-stable.lock` deliberately suppresses master image publication and blocks tags instead of falling back to dev.
- Create `OPENSAOLA_AUTOMATION_TOKEN` as an Actions secret backed by a dedicated fine-grained bot PAT. Its repository permissions must include **Contents: read and write**, **Pull requests: read and write**, and **Actions: read**. It must also have repository **Metadata: read** and permission to apply the pre-created stable label (for fine-grained PATs, label operations are covered by Pull requests write plus Metadata read). The workflows fail closed when it is absent and deliberately do not substitute `GITHUB_TOKEN`, because bot PRs must trigger normal downstream CI.
- Enable repository auto-merge and protect both `dev` and `master` as PR-only branches. Require the relevant CI and Docker checks and forbid direct pushes, including from the automation bot.
- Pre-create the exact label `automation:saola-cli-stable` and restrict who may apply it. Promotion treats that label as the stable candidate boundary.
- Set repository variable `OPENSAOLA_AUTOMATION_LOGIN` to the dedicated bot login. Candidate selection fails closed unless its PR author and candidate-only diff match the contract; master checks likewise allow `build/saola-cli-stable.lock` to change only in the bot's deterministic promotion PR.
- Keep the default soak at 24 hours for scheduled runs. A manual run may explicitly choose another non-negative soak value for an authorized incident response; that exception should be auditable.
- Configure an operational stable-release denylist or rollback decision outside these workflows before activation. To stop a candidate, disable auto-merge/remove the stable label or close its promotion PR. Automated stable events accept only the latest published release; rollback must therefore use a normal reviewed lock-only PR pointing to a previously verified stable version. Do not edit the lock to a floating ref.

No local validation can prove that secrets, token scopes, label policy, branch protection, auto-merge, or hosted-runner behavior are configured correctly; verify them in GitHub before relying on this path.

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
