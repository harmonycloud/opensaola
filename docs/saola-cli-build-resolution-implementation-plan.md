# Build-Time Saola CLI Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Decouple Saola CLI releases from OpenSaola, then make the OpenSaola Docker workflow resolve and verify the latest final Saola CLI Release while keeping release-tag builds pinned by a committed stable lock.

**Architecture:** Saola CLI publishes immutable multi-architecture Releases without a downstream credential. OpenSaola owns dependency discovery through a deterministic resolver. A `dev` Docker run may update only `build/saola-cli-stable.lock` and explicitly re-dispatch itself; `master` and `v*` runs are read-only.

**Tech Stack:** GitHub Actions, Bash 5, `gh`, `git`, `jq`, Go 1.26, Docker Buildx, GHCR

## Global Constraints

- Do not track anything under `docs/superpowers/`; this plan remains in normal `docs/`.
- Remove `OPENSAOLA_DISPATCH_TOKEN`, `repository_dispatch`, scheduled polling, personal PATs, stable-candidate PRs, and the 24-hour promotion path.
- Eligible Saola CLI versions are published, non-draft, non-prerelease `vMAJOR.MINOR.PATCH` Releases.
- Latest means the numerically highest eligible SemVer.
- One Docker run resolves one exact repository, version, commit, channel, and source timestamp for both amd64 and arm64.
- External Saola CLI builds must run with `GITHUB_OUTPUT`, `GITHUB_ENV`, `GITHUB_PATH`, and `GITHUB_STEP_SUMMARY` removed from their environment.
- Only `dev` may write the stable lock. `master` and `v*` are read-only.
- A formal OpenSaola tag uses the stable lock in the tagged commit and never replaces it dynamically.
- Official image platforms remain exactly `linux/amd64` and `linux/arm64`; OCI CLI labels, provenance, and SBOM remain enabled.

---

## File Map

### Saola CLI

- Modify `.github/workflows/release.yml`: remove downstream credential validation and dispatch.
- Modify `.github/workflows/ci.yml`: run the release contract test on pull requests.
- Modify `hack/release-metadata_test.sh`: prohibit downstream coupling and preserve asset assertions.
- Modify `README.md` and `README_zh.md`: document independent publication.

### OpenSaola

- Create `hack/resolve-saola-cli-release.sh`: select, verify, rebuild, and export a stable lock.
- Create `hack/resolve-saola-cli-release_test.sh`: deterministic no-network fixture tests.
- Modify `hack/saola-cli-lock.sh`: accept only final stable locks.
- Modify `hack/saola-cli-lock_test.sh`: enforce the new workflow contract.
- Modify `.github/workflows/docker.yml`: resolve once, optionally sync `dev`, then build.
- Delete `.github/workflows/saola-cli-update.yml` and `.github/workflows/saola-cli-promote.yml`.
- Delete `build/saola-cli-dev.lock`; prohibit `build/saola-cli-stable-candidate.lock`.
- Modify `Makefile`: use only `build/saola-cli-stable.lock` and run the shell contracts.
- Modify `docs/release-process.md`, `docs/release-process_zh.md`, and the approved design status.

---

### Task 1: Decouple Saola CLI Release Publication

**Repository:** `/Users/yaozekai/Desktop/go/OpenSaola/saola-cli`

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `.github/workflows/ci.yml`
- Modify: `hack/release-metadata_test.sh`
- Modify: `README.md`
- Modify: `README_zh.md`

**Interfaces:**
- Consumes: `hack/release-metadata.sh <git-ref> <full-commit> harmonycloud/saola-cli`
- Produces: independent `main` artifacts and immutable `v*` Releases; no cross-repository event

- [ ] **Step 1: Add failing release-workflow assertions**

Add this before the final PASS in `hack/release-metadata_test.sh`:

~~~bash
for forbidden in \
  OPENSAOLA_DISPATCH_TOKEN \
  repository_dispatch \
  harmonycloud/opensaola/dispatches \
  'Dispatch immutable OpenSaola update' \
  'Validate dispatch credential' \
  event_type \
  saola-cli-dev \
  saola-cli-stable; do
  if grep -Fq "${forbidden}" "${workflow}"; then
    fail "release workflow still contains downstream coupling: ${forbidden}"
  fi
done

for required in \
  "tags: ['v*']" \
  'make release-build' \
  'make release-checksums' \
  'saola-linux-amd64' \
  'saola-linux-arm64' \
  'attest-build-provenance' \
  'cosign sign-blob' \
  'actions/upload-artifact' \
  'gh release create' \
  'gh release download' \
  'gh release edit'; do
  grep -Fq "${required}" "${workflow}" || fail "release workflow lost required contract: ${required}"
done
~~~

- [ ] **Step 2: Prove the test fails for the current workflow**

Run `bash hack/release-metadata_test.sh`.

Expected: non-zero with `release workflow still contains downstream coupling: OPENSAOLA_DISPATCH_TOKEN`.

- [ ] **Step 3: Remove only the downstream coupling**

In `.github/workflows/release.yml`, remove `event_type` from metadata outputs, remove its derivation case, remove `Validate dispatch credential`, and remove `Dispatch immutable OpenSaola update`. Keep the build job permissions and all test/build/checksum/SBOM/attestation/signing/draft-release steps unchanged.

- [ ] **Step 4: Run the contract during PR CI**

Add this step before Go tests in `.github/workflows/ci.yml`:

~~~yaml
- name: Test release workflow contract
  run: bash hack/release-metadata_test.sh
~~~

- [ ] **Step 5: Update both README release sections**

Both languages must state:

~~~text
main push -> dev-<12-char-sha> workflow artifact only
vMAJOR.MINOR.PATCH tag -> immutable GitHub Release with both Linux binaries, SHA256SUMS, SBOMs, signatures, and attestations
OpenSaola -> resolves published final Releases from its own Docker workflow
No OPENSAOLA_DISPATCH_TOKEN and no repository_dispatch
~~~

- [ ] **Step 6: Validate and commit**

Run:

~~~bash
bash -n hack/release-metadata.sh
bash -n hack/release-metadata_test.sh
bash hack/release-metadata_test.sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 \
  .github/workflows/ci.yml .github/workflows/release.yml
make test
go test -race ./... -count=1
make build
git diff --check
~~~

Then commit:

~~~bash
git add .github/workflows/release.yml .github/workflows/ci.yml \
  hack/release-metadata_test.sh README.md README_zh.md
git commit -m "ci: decouple saola CLI releases from OpenSaola"
~~~

---

### Task 2: Add a Deterministic Release Resolver

**Repository:** `/Users/yaozekai/Desktop/go/OpenSaola/opensaola`

**Files:**
- Create: `hack/resolve-saola-cli-release.sh`
- Create: `hack/resolve-saola-cli-release_test.sh`
- Modify: `hack/saola-cli-lock.sh`
- Modify: `hack/saola-cli-lock_test.sh`

**Interfaces:**
- `hack/resolve-saola-cli-release.sh resolve-latest <output-lock>`
- `hack/resolve-saola-cli-release.sh verify-lock <lock-file>`
- Test overrides: `SAOLA_CLI_RELEASES_JSON_FILE`, `SAOLA_CLI_TAG_REFS_FILE`, `SAOLA_CLI_RELEASE_DIR`, `SAOLA_CLI_SOURCE_DIR`
- Lock order: `repository`, `version`, `commit`, `channel`, `source_date_epoch`

- [ ] **Step 1: Write fixture tests before the resolver**

The test creates a temporary Git source repository with deterministic `release-build` and `release-checksums` Make targets, plus a release asset directory. Its Release fixture is:

~~~json
[
  {"tag_name":"v1.0.0","draft":false,"prerelease":false},
  {"tag_name":"v1.10.0","draft":false,"prerelease":false},
  {"tag_name":"v2.0.0-rc.1","draft":false,"prerelease":true},
  {"tag_name":"v9.0.0","draft":true,"prerelease":false},
  {"tag_name":"not-semver","draft":false,"prerelease":false}
]
~~~

The passing path must execute:

~~~bash
"${resolver}" resolve-latest "${tmp_dir}/resolved.lock"
"${lock_helper}" validate "${tmp_dir}/resolved.lock"
[[ "$("${lock_helper}" get "${tmp_dir}/resolved.lock" version)" = v1.10.0 ]]
[[ "$("${lock_helper}" get "${tmp_dir}/resolved.lock" channel)" = stable ]]
"${resolver}" verify-lock "${tmp_dir}/resolved.lock"
~~~

Add explicit rejection fixtures for: no eligible Release, mismatched tag commit, missing `SHA256SUMS`, missing architecture asset, published checksum mismatch, rebuilt checksum mismatch, and invalid lock.

- [ ] **Step 2: Prove the resolver test initially fails**

Run `bash hack/resolve-saola-cli-release_test.sh`.

Expected: non-zero because the resolver does not exist.

- [ ] **Step 3: Implement the resolver commands**

Use this public dispatch:

~~~bash
case "${1:-}" in
  resolve-latest)
    [[ "$#" -eq 2 ]] || usage
    resolve_latest "$2"
    ;;
  verify-lock)
    [[ "$#" -eq 2 ]] || usage
    verify_lock "$2"
    ;;
  *) usage ;;
esac
~~~

`resolve_latest` must use all paginated Releases, filter eligible final tags, parse numeric major/minor/patch tuples, choose the maximum, resolve the peeled tag commit, derive the commit epoch, write the five fields, call `verify_lock`, and atomically move the result to the requested path.

`verify_lock` must validate the lock, require the same-name tag to peel to the exact commit, require a matching published final Release, download exactly `SHA256SUMS` and both Linux binaries, validate the published checksums, rebuild both architectures from the exact commit with `VERSION`, `GIT_COMMIT`, and `SOURCE_DATE_EPOCH`, then compare both rebuilt checksums. The external build must be invoked through `env -u GITHUB_OUTPUT -u GITHUB_ENV -u GITHUB_PATH -u GITHUB_STEP_SUMMARY`. Fixture inputs replace external reads but use the same validation code.

- [ ] **Step 4: Make the lock helper stable-only**

Replace its channel case with:

~~~bash
[[ "${channel}" = stable ]] || die 'channel must be stable'
validate_stable_version "${version}" || die 'stable version must be a final vMAJOR.MINOR.PATCH release tag'
~~~

In `hack/saola-cli-lock_test.sh`, make the strict stable lock the valid fixture, assert that `channel=dev` and `version=dev-<sha>` are rejected, and keep the duplicate/missing/unknown/unsafe-value tests unchanged.

- [ ] **Step 5: Validate and commit**

Run:

~~~bash
bash -n hack/resolve-saola-cli-release.sh
bash -n hack/resolve-saola-cli-release_test.sh
bash -n hack/saola-cli-lock.sh
bash hack/resolve-saola-cli-release_test.sh
bash hack/saola-cli-lock_test.sh
~~~

Expected: `PASS: saola-cli release resolver`.

Commit:

~~~bash
git add hack/resolve-saola-cli-release.sh \
  hack/resolve-saola-cli-release_test.sh hack/saola-cli-lock.sh \
  hack/saola-cli-lock_test.sh
git commit -m "feat: resolve latest stable saola CLI release"
~~~

---

### Task 3: Integrate Resolution into Docker Builds

**Repository:** `/Users/yaozekai/Desktop/go/OpenSaola/opensaola`

**Files:**
- Modify: `.github/workflows/docker.yml`
- Delete: `.github/workflows/saola-cli-update.yml`
- Delete: `.github/workflows/saola-cli-promote.yml`
- Delete: `build/saola-cli-dev.lock`
- Modify: `Makefile`
- Modify: `hack/saola-cli-lock_test.sh`

**Interfaces:**
- Resolver outputs: `repository`, `version`, `commit`, `channel`, `source_date_epoch`, `lock_changed`, `build_enabled`
- Optional mutation: a direct `dev` commit that changes only `build/saola-cli-stable.lock`

- [ ] **Step 1: Rewrite static workflow assertions first**

Require legacy files to be absent, require both resolver commands and stable lock in Docker workflow, require the dev sync job to have `contents: write` and `actions: write`, and require `gh workflow run docker.yml --ref dev`. Reject `repository_dispatch`, both automation tokens/logins, candidate paths, and dev lock paths.

- [ ] **Step 2: Prove the lock contract fails against the current tree**

Run `bash hack/saola-cli-lock_test.sh`.

Expected: non-zero because the legacy workflows and dev lock still exist.

- [ ] **Step 3: Make local builds stable-lock-only and attach tests**

Use:

~~~make
SAOLA_CLI_LOCK ?= build/saola-cli-stable.lock
SAOLA_CLI_LOCK_HELPER ?= hack/saola-cli-lock.sh

.PHONY: test-release-automation
test-release-automation:
	bash hack/resolve-saola-cli-release_test.sh
	bash hack/saola-cli-lock_test.sh

test: test-makefile test-release-automation manifests generate fmt vet
~~~

Remove `SAOLA_CLI_CHANNEL`.

- [ ] **Step 4: Add the read-only `resolve-cli` job**

For non-PR events, use this route:

~~~bash
case "${GITHUB_REF}" in
  refs/heads/dev|refs/heads/master)
    hack/resolve-saola-cli-release.sh resolve-latest "${RUNNER_TEMP}/saola-cli.lock"
    ;;
  refs/tags/v*)
    cp build/saola-cli-stable.lock "${RUNNER_TEMP}/saola-cli.lock"
    hack/resolve-saola-cli-release.sh verify-lock "${RUNNER_TEMP}/saola-cli.lock"
    ;;
  *) exit 1 ;;
esac
~~~

For branches, compare with the tracked stable lock. On stale `master`, fail. On stale `dev`, set `lock_changed=true` and `build_enabled=false`. Otherwise set `build_enabled=true`. Export the five validated lock fields as job outputs. Serialize dev runs by ref with `cancel-in-progress: false`.

- [ ] **Step 5: Add the isolated `sync-dev-lock` job**

Grant only:

~~~yaml
permissions:
  actions: write
  contents: write
~~~

When `dev` is stale, reconstruct the five-field lock from trusted resolver outputs, validate it, ensure the staged diff contains only `build/saola-cli-stable.lock`, then execute:

~~~bash
git fetch origin dev
git checkout -B dev origin/dev
git -c core.hooksPath=/dev/null commit --no-gpg-sign \
  -m "build: pin bundled saola CLI ${CLI_VERSION}"
git push origin HEAD:dev
gh workflow run docker.yml --ref dev
~~~

The current run must not publish. The explicitly dispatched run starts from updated `dev` and resolves again.

- [ ] **Step 6: Simplify PR and publish paths**

PR builds use `build/saola-cli-stable.lock` when present and otherwise perform the existing bootstrap no-op. Remove automation-author, promotion-branch, candidate-lock, and dev-lock routing. Delete `stable-candidate-build`. Make publish depend on `resolve-cli` and run only when `build_enabled == 'true'`. Use the five resolver outputs for source checkout, Docker arguments, and OCI labels. Preserve QEMU, Buildx, GHCR login, multiarch, cache, provenance, and SBOM.

- [ ] **Step 7: Delete legacy files**

Delete exactly:

~~~text
.github/workflows/saola-cli-update.yml
.github/workflows/saola-cli-promote.yml
build/saola-cli-dev.lock
~~~

Keep `build/saola-cli-stable.lock`; it is generated after Saola CLI `v1.0.0` exists.

- [ ] **Step 8: Validate and commit**

Run:

~~~bash
bash hack/resolve-saola-cli-release_test.sh
bash hack/saola-cli-lock_test.sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 \
  .github/workflows/docker.yml .github/workflows/helm-chart.yml
make test
make helm-check
git diff --check
~~~

Commit:

~~~bash
git add .github/workflows/docker.yml Makefile hack/saola-cli-lock_test.sh
git add -u .github/workflows build
git commit -m "ci: resolve saola CLI during image builds"
~~~

---

### Task 4: Update Documentation and Run Full Regression

**Repository:** `/Users/yaozekai/Desktop/go/OpenSaola/opensaola`

**Files:**
- Modify: `docs/release-process.md`
- Modify: `docs/release-process_zh.md`
- Modify: `docs/saola-cli-build-resolution-design.md`

- [ ] **Step 1: Replace old channel/promotion documentation**

Both languages must state: Saola CLI publishes independently; OpenSaola dev resolves the highest final SemVer Release; stale dev lock is committed with `GITHUB_TOKEN` and the Docker workflow is explicitly re-dispatched; master never writes; formal tags verify the lock in the tag commit; no dispatch token, automation token/login, label, schedule, candidate, or soak is required.

- [ ] **Step 2: Mark the design implemented only after code tests pass**

Set `状态：已实现并验证`.

- [ ] **Step 3: Scan obsolete contracts and run regression**

Run:

~~~bash
! rg -n 'OPENSAOLA_DISPATCH_TOKEN|OPENSAOLA_AUTOMATION_TOKEN|OPENSAOLA_AUTOMATION_LOGIN|repository_dispatch|saola-cli-stable-candidate|saola-cli-dev.lock|24 hour|24 小时' \
  .github/workflows build docs/release-process.md docs/release-process_zh.md
make test
make test-race
make test-envtest
make helm-check
bash hack/resolve-saola-cli-release_test.sh
bash hack/saola-cli-lock_test.sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/*.yml
git diff --check
~~~

- [ ] **Step 4: Commit documentation**

~~~bash
git add docs/release-process.md docs/release-process_zh.md \
  docs/saola-cli-build-resolution-design.md
git commit -m "docs: describe build-time saola CLI resolution"
~~~

---

### Task 5: Publish Saola CLI v1.0.0 and OpenSaola v1.1.0

**Repositories:** both `saola-cli` and `opensaola`

- [ ] **Step 1: Final remote-mutation checkpoint**

Report local and remote branch SHAs, proposed tag targets, clean status, and test verdicts. Do not push before explicit confirmation.

- [ ] **Step 2: Publish Saola CLI code and tag**

Push only intended GitHub refs:

~~~bash
git push github main
git tag v1.0.0 "$(git rev-parse main)"
git push github refs/tags/v1.0.0
~~~

Require a successful Release workflow, exact tag commit, non-draft/non-prerelease Release, both binaries, `SHA256SUMS`, two SBOMs, five Sigstore bundles, and five attestations.

- [ ] **Step 3: Create the OpenSaola stable lock from v1.0.0**

~~~bash
hack/resolve-saola-cli-release.sh resolve-latest build/saola-cli-stable.lock
hack/saola-cli-lock.sh validate build/saola-cli-stable.lock
[[ "$(hack/saola-cli-lock.sh get build/saola-cli-stable.lock version)" = v1.0.0 ]]
git add build/saola-cli-stable.lock
git commit -m "build: pin bundled saola CLI v1.0.0"
~~~

- [ ] **Step 4: Merge and push OpenSaola branches**

Run full regression on `dev`, merge `dev` into local `master` with `--no-ff`, rerun `make test` and `make helm-check`, then push only:

~~~bash
git push github dev
git push github master
~~~

Require remote CI and Docker success on exact SHAs.

- [ ] **Step 5: Publish OpenSaola v1.1.0**

~~~bash
git tag v1.1.0 "$(git rev-parse master)"
git push github refs/tags/v1.1.0
~~~

Require Docker and Helm Chart success. Verify the `v1.1.0`, `1.1.0`, `1.1`, and `latest` image tags plus OCI chart `1.1.0`. Inspect amd64/arm64 images and require `saola version` to report `v1.0.0` and the exact Saola CLI release commit.

- [ ] **Step 6: Final remote audit**

Confirm no failed/in-progress release runs remain, no obsolete automation PR/branch remains, worktrees are clean, and both remote tags point to verified commits. Record workflow and Release URLs in the handoff.
