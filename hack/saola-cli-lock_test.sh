#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
helper="${script_dir}/saola-cli-lock.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

commit="dfe685bacbeb036a06316561a1e982813763e2e4"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

write_lock() {
  local path="$1"
  shift
  printf '%s\n' "$@" >"${path}"
}

assert_rejected() {
  local path="$1"
  if "${helper}" validate "${path}" >/dev/null 2>&1; then
    fail "expected invalid lock to be rejected: ${path}"
  fi
}

valid_lock="${tmp_dir}/valid.lock"
write_lock "${valid_lock}" \
  'repository=harmonycloud/saola-cli' \
  'version=dev-dfe685bacbeb' \
  "commit=${commit}" \
  'channel=dev' \
  'source_date_epoch=1782812176'

"${helper}" validate "${valid_lock}"
[[ "$("${helper}" get "${valid_lock}" repository)" == 'harmonycloud/saola-cli' ]] || fail 'repository extraction failed'
[[ "$("${helper}" get "${valid_lock}" version)" == 'dev-dfe685bacbeb' ]] || fail 'version extraction failed'
[[ "$("${helper}" get "${valid_lock}" commit)" == "${commit}" ]] || fail 'commit extraction failed'
[[ "$("${helper}" get "${valid_lock}" channel)" == 'dev' ]] || fail 'channel extraction failed'
[[ "$("${helper}" get "${valid_lock}" source_date_epoch)" == '1782812176' ]] || fail 'epoch extraction failed'

duplicate="${tmp_dir}/duplicate.lock"
cp "${valid_lock}" "${duplicate}"
printf 'channel=dev\n' >>"${duplicate}"
assert_rejected "${duplicate}"

missing="${tmp_dir}/missing.lock"
sed '/^version=/d' "${valid_lock}" >"${missing}"
assert_rejected "${missing}"

unknown="${tmp_dir}/unknown.lock"
cp "${valid_lock}" "${unknown}"
printf 'unexpected=value\n' >>"${unknown}"
assert_rejected "${unknown}"

bad_repository="${tmp_dir}/bad-repository.lock"
sed 's#^repository=.*#repository=other/saola-cli#' "${valid_lock}" >"${bad_repository}"
assert_rejected "${bad_repository}"

bad_channel="${tmp_dir}/bad-channel.lock"
sed 's/^channel=.*/channel=preview/' "${valid_lock}" >"${bad_channel}"
assert_rejected "${bad_channel}"

bad_dev_version="${tmp_dir}/bad-dev-version.lock"
sed 's/^version=.*/version=v1.2.3/' "${valid_lock}" >"${bad_dev_version}"
assert_rejected "${bad_dev_version}"

stable_lock="${tmp_dir}/stable.lock"
write_lock "${stable_lock}" \
  'repository=harmonycloud/saola-cli' \
  'version=v1.2.3-rc.1' \
  "commit=${commit}" \
  'channel=stable' \
  'source_date_epoch=1782812176'
"${helper}" validate "${stable_lock}"

bad_stable_version="${tmp_dir}/bad-stable-version.lock"
sed 's/^version=.*/version=dev-dfe685bacbeb/' "${stable_lock}" >"${bad_stable_version}"
assert_rejected "${bad_stable_version}"

bad_stable_prerelease="${tmp_dir}/bad-stable-prerelease.lock"
sed 's/^version=.*/version=v1.2.3-01/' "${stable_lock}" >"${bad_stable_prerelease}"
assert_rejected "${bad_stable_prerelease}"

bad_stable_build_metadata="${tmp_dir}/bad-stable-build-metadata.lock"
sed 's/^version=.*/version=v1.2.3+build.1/' "${stable_lock}" >"${bad_stable_build_metadata}"
assert_rejected "${bad_stable_build_metadata}"

bad_commit="${tmp_dir}/bad-commit.lock"
sed 's/^commit=.*/commit=dfe685b/' "${valid_lock}" >"${bad_commit}"
assert_rejected "${bad_commit}"

bad_epoch="${tmp_dir}/bad-epoch.lock"
sed 's/^source_date_epoch=.*/source_date_epoch=-1/' "${valid_lock}" >"${bad_epoch}"
assert_rejected "${bad_epoch}"

marker="${tmp_dir}/sourced"
unsafe_value="${tmp_dir}/unsafe-value.lock"
sed "s#^repository=.*#repository=\$(touch ${marker})#" "${valid_lock}" >"${unsafe_value}"
assert_rejected "${unsafe_value}"
[[ ! -e "${marker}" ]] || fail 'lock content was executed'

if "${helper}" get "${valid_lock}" 'repository;touch-pwned' >/dev/null 2>&1; then
  fail 'unsafe key was accepted'
fi
[[ ! -e "${tmp_dir}/touch-pwned" ]] || fail 'get key was executed'

repo_root="$(cd "${script_dir}/.." && pwd)"
docker_build_output="$(TMPDIR="${tmp_dir}" make -s -C "${repo_root}" docker-build CONTAINER_TOOL=echo IMG=controller:test)"
[[ "${docker_build_output}" != *'git#'* ]] || fail 'docker-build still uses an unusable remote git#SHA context'
docker_build_context="$(sed -n 's/.*--build-context saola-cli=\([^ ]*\).*/\1/p' <<<"${docker_build_output}")"
[[ "${docker_build_context}" == /* ]] || fail 'docker-build default context is not an absolute temporary local directory'
[[ ! -e "${docker_build_context}" ]] || fail 'docker-build temporary context was not cleaned after success'
[[ "${docker_build_output}" == *"--build-arg SAOLA_CLI_VERSION=dev-dfe685bacbeb"* ]] || fail 'docker-build is missing the locked CLI version'
[[ "${docker_build_output}" == *"--build-arg SAOLA_CLI_COMMIT=${commit}"* ]] || fail 'docker-build is missing the locked CLI commit'
[[ "${docker_build_output}" == *'--platform=linux/amd64'* ]] || fail 'docker-build default platform is not linux/amd64'
if make -s -C "${repo_root}" docker-build CONTAINER_TOOL=echo IMG=controller:test DOCKER_PLATFORM=linux/s390x >/dev/null 2>&1; then
  fail 'docker-build accepted an unsupported platform'
fi

docker_buildx_output="$(TMPDIR="${tmp_dir}" make -s -C "${repo_root}" docker-buildx CONTAINER_TOOL=echo IMG=controller:test)"
[[ "${docker_buildx_output}" == *'--platform=linux/amd64,linux/arm64'* ]] || fail 'docker-buildx platforms are not exactly amd64 and arm64'
[[ "${docker_buildx_output}" != *'Dockerfile.cross'* ]] || fail 'docker-buildx still generates Dockerfile.cross'
[[ "${docker_buildx_output}" != *'git#'* ]] || fail 'docker-buildx still uses an unusable remote git#SHA context'
docker_buildx_context="$(sed -n 's/.*--build-context saola-cli=\([^ ]*\).*/\1/p' <<<"${docker_buildx_output}")"
[[ "${docker_buildx_context}" == /* ]] || fail 'docker-buildx default context is not an absolute temporary local directory'
[[ ! -e "${docker_buildx_context}" ]] || fail 'docker-buildx temporary context was not cleaned after success'

override_output="$(make -s -C "${repo_root}" docker-build CONTAINER_TOOL=echo IMG=controller:test \
  SAOLA_CLI_REPOSITORY=attacker/saola-cli \
  SAOLA_CLI_VERSION=v9.9.9 \
  SAOLA_CLI_COMMIT=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  SAOLA_CLI_SOURCE_DATE_EPOCH=1 \
  'SAOLA_CLI_BUILD_ARGS=--build-arg PWNED=true')"
[[ "${override_output}" != *'attacker/saola-cli'* && "${override_output}" != *'git#'* ]] || fail 'command line overrode the lock-derived repository or selected a remote context'
[[ "${override_output}" == *'--build-arg SAOLA_CLI_VERSION=dev-dfe685bacbeb'* ]] || fail 'command line overrode the lock-derived version'
[[ "${override_output}" == *"--build-arg SAOLA_CLI_COMMIT=${commit}"* ]] || fail 'command line overrode the lock-derived commit build arg'
[[ "${override_output}" == *'--build-arg SAOLA_CLI_SOURCE_DATE_EPOCH=1782812176'* ]] || fail 'command line overrode the lock-derived source epoch'
[[ "${override_output}" != *'PWNED=true'* ]] || fail 'command line overrode SAOLA_CLI_BUILD_ARGS'

local_context_output="$(make -s -C "${repo_root}" docker-build CONTAINER_TOOL=echo IMG=controller:test SAOLA_CLI_CONTEXT=../saola-cli)"
[[ "${local_context_output}" != *'--build-context saola-cli=../saola-cli'* ]] || fail 'explicit SAOLA_CLI_CONTEXT was passed through without a clean archive'
local_build_context="$(sed -n 's/.*--build-context saola-cli=\([^ ]*\).*/\1/p' <<<"${local_context_output}")"
[[ "${local_build_context}" == /* && ! -e "${local_build_context}" ]] || fail 'explicit source repo did not use a cleaned temporary archive'
[[ "${local_context_output}" == *"--build-arg SAOLA_CLI_COMMIT=${commit}"* ]] || fail 'local context override changed the locked commit metadata'

platform_override_output="$(make -s -C "${repo_root}" docker-buildx CONTAINER_TOOL=echo IMG=controller:test PLATFORMS=linux/s390x)"
[[ "${platform_override_output}" == *'--platform=linux/amd64,linux/arm64'* ]] || fail 'command line overrode the fixed buildx platforms'
[[ "${platform_override_output}" != *'linux/s390x'* ]] || fail 'unsupported buildx platform reached the command'

inspector="${tmp_dir}/inspect-container-context.sh"
cat >"${inspector}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

context=''
previous=''
for argument in "$@"; do
  if [[ "${previous}" == '--build-context' ]]; then
    context="${argument#saola-cli=}"
    break
  fi
  previous="${argument}"
done
[[ -n "${context}" && -d "${context}" ]]
[[ ! -e "${context}/.git" ]]

expected="$(mktemp -d)"
trap 'rm -rf "${expected}"' EXIT
git -C "${EXPECTED_REPOSITORY}" archive "${EXPECTED_COMMIT}" | tar -x -C "${expected}"
diff -qr "${expected}" "${context}" >/dev/null
printf 'INSPECTED_CONTEXT=%s\n' "${context}"
[[ "${FAIL_AFTER_INSPECT:-false}" != 'true' ]] || exit 42
EOF
chmod +x "${inspector}"

inspected_output="$(EXPECTED_REPOSITORY="${repo_root}/../saola-cli" EXPECTED_COMMIT="${commit}" TMPDIR="${tmp_dir}" \
  make -s -C "${repo_root}" docker-build CONTAINER_TOOL="${inspector}" IMG=controller:test)"
inspected_context="${inspected_output#INSPECTED_CONTEXT=}"
[[ "${inspected_context}" == /* ]] || fail 'inspector did not receive an absolute temporary local context'
[[ ! -e "${inspected_context}" ]] || fail 'inspected docker-build context was not cleaned'

inspected_buildx_output="$(EXPECTED_REPOSITORY="${repo_root}/../saola-cli" EXPECTED_COMMIT="${commit}" TMPDIR="${tmp_dir}" \
  make -s -C "${repo_root}" docker-buildx CONTAINER_TOOL="${inspector}" IMG=controller:test)"
inspected_buildx_context="${inspected_buildx_output#INSPECTED_CONTEXT=}"
[[ ! -e "${inspected_buildx_context}" ]] || fail 'inspected docker-buildx context was not cleaned'

inspected_explicit_output="$(EXPECTED_REPOSITORY="${repo_root}/../saola-cli" EXPECTED_COMMIT="${commit}" TMPDIR="${tmp_dir}" \
  make -s -C "${repo_root}" docker-build CONTAINER_TOOL="${inspector}" IMG=controller:test SAOLA_CLI_CONTEXT=../saola-cli)"
inspected_explicit_context="${inspected_explicit_output#INSPECTED_CONTEXT=}"
[[ "${inspected_explicit_context}" == "${tmp_dir}"/opensaola-saola-cli.* ]] || fail 'explicit source repo did not produce a temporary archive context'
[[ ! -e "${inspected_explicit_context}" ]] || fail 'explicit source repo archive was not cleaned'

if failed_output="$(EXPECTED_REPOSITORY="${repo_root}/../saola-cli" EXPECTED_COMMIT="${commit}" FAIL_AFTER_INSPECT=true TMPDIR="${tmp_dir}" \
  make -s -C "${repo_root}" docker-build CONTAINER_TOOL="${inspector}" IMG=controller:test 2>&1)"; then
  fail 'expected fake container failure'
fi
failed_context="$(sed -n 's/^INSPECTED_CONTEXT=//p' <<<"${failed_output}")"
[[ -n "${failed_context}" && ! -e "${failed_context}" ]] || fail 'docker-build temporary context was not cleaned after failure'

unrelated_repo="${tmp_dir}/unrelated-repo"
mkdir -p "${unrelated_repo}"
git -C "${unrelated_repo}" init -q
git -C "${unrelated_repo}" config user.name 'Lock Test'
git -C "${unrelated_repo}" config user.email 'lock-test@example.invalid'
printf 'unrelated\n' >"${unrelated_repo}/README"
git -C "${unrelated_repo}" add README
git -C "${unrelated_repo}" commit -q -m unrelated
never_called="${tmp_dir}/container-tool-called"
never_tool="${tmp_dir}/never-container-tool.sh"
cat >"${never_tool}" <<EOF
#!/usr/bin/env bash
touch '${never_called}'
EOF
chmod +x "${never_tool}"
if TMPDIR="${tmp_dir}" make -s -C "${repo_root}" docker-build CONTAINER_TOOL="${never_tool}" IMG=controller:test SAOLA_CLI_CONTEXT="${unrelated_repo}" >/dev/null 2>&1; then
  fail 'source repo without the locked commit was accepted'
fi
[[ ! -e "${never_called}" ]] || fail 'container tool ran before the locked commit was verified'
if compgen -G "${tmp_dir}/opensaola-saola-cli.*" >/dev/null; then
  fail 'temporary archive leaked after locked commit verification failed'
fi

dockerfile="${repo_root}/Dockerfile"
cli_stage_line="$(grep -n '^FROM .* AS saola-cli-builder$' "${dockerfile}" | cut -d: -f1)"
kubectl_download_line="$(grep -n '^RUN curl -LO ' "${dockerfile}" | cut -d: -f1)"
[[ -n "${cli_stage_line}" && -n "${kubectl_download_line}" && "${cli_stage_line}" -gt "${kubectl_download_line}" ]] || fail 'saola-cli stage interrupts the manager/kubectl builder stage'

assert_workflow_contains() {
  local workflow="$1"
  local contract="$2"
  local pattern="$3"
  grep -Eq -- "${pattern}" "${workflow}" || fail "${contract}: ${workflow}"
}

assert_workflow_excludes() {
  local workflow="$1"
  local contract="$2"
  local pattern="$3"
  if grep -Eq -- "${pattern}" "${workflow}"; then
    fail "${contract}: ${workflow}"
  fi
}

update_workflow="${repo_root}/.github/workflows/saola-cli-update.yml"
promote_workflow="${repo_root}/.github/workflows/saola-cli-promote.yml"
docker_workflow="${repo_root}/.github/workflows/docker.yml"

[[ -f "${update_workflow}" ]] || fail 'missing saola-cli update workflow'
assert_workflow_contains "${update_workflow}" 'update workflow does not accept both dispatch event types' 'types:.*saola-cli-dev.*saola-cli-stable'
assert_workflow_contains "${update_workflow}" 'source validation is not isolated in its own job' '^  validate-source:'
assert_workflow_contains "${update_workflow}" 'mutation job does not depend on source validation' '^  update-dev:|needs: validate-source'
validate_job="$(sed -n '/^  validate-source:/,/^  update-dev:/p' "${update_workflow}")"
[[ "${validate_job}" != *'OPENSAOLA_AUTOMATION_TOKEN'* ]] || fail 'validate-source job can access the OpenSaola automation token'
[[ "${validate_job}" == *'persist-credentials: false'* ]] || fail 'external source checkout persists credentials'
[[ "${validate_job}" == *'outputs:'* ]] || fail 'validate-source job does not expose strict outputs'
assert_workflow_contains "${update_workflow}" 'job outputs are not sourced from trusted identity' 'repository:.*steps\.identity\.outputs\.repository'
verified_step="$(sed -n '/name: Rebuild and verify both published checksums/,/^  update-dev:/p' "${update_workflow}")"
[[ "${verified_step}" != *'>>"${GITHUB_OUTPUT}"'* ]] || fail 'external build step can write trusted identity outputs'
assert_workflow_contains "${update_workflow}" 'external make does not clear workflow command files' 'env -u GITHUB_OUTPUT -u GITHUB_ENV -u GITHUB_PATH -u GITHUB_STEP_SUMMARY'
assert_workflow_contains "${update_workflow}" 'release query does not paginate all releases' 'gh api --paginate'
assert_workflow_contains "${update_workflow}" 'source epoch is not bound to commit timestamp' 'committer\.date|commit_epoch'
assert_workflow_contains "${update_workflow}" 'stable payload checksums are not compared with Release SHA256SUMS' 'release_amd64.*CLI_AMD64_SHA256'
identity_step="$(sed -n '/name: Validate immutable event payload/,/name: Check out the exact Saola CLI source/p' "${update_workflow}")"
[[ "${identity_step}" == *'GH_TOKEN: ${{ github.token }}'* ]] || fail 'identity step does not use the read-only GitHub token'
[[ "${validate_job}" != *'token: ${{ github.token }}'* ]] || fail 'GitHub token leaked into checkout configuration'
assert_workflow_contains "${update_workflow}" 'manual update dispatch is missing repository input' 'repository:'
assert_workflow_contains "${update_workflow}" 'manual update dispatch is missing both checksums' 'amd64_sha256:'
assert_workflow_contains "${update_workflow}" 'manual update dispatch is missing both checksums' 'arm64_sha256:'
assert_workflow_contains "${update_workflow}" 'update workflow does not fail closed without the automation token' 'OPENSAOLA_AUTOMATION_TOKEN.*required'
assert_workflow_contains "${update_workflow}" 'update workflow does not verify the immutable source revision' 'git ls-remote'
assert_workflow_contains "${update_workflow}" 'dev event is not bound to the current main ref' 'refs/heads/main'
assert_workflow_contains "${update_workflow}" 'stable event is not bound to its exact tag ref' 'refs/tags/.*CLI_VERSION'
assert_workflow_contains "${update_workflow}" 'stable tag is not peeled before commit comparison' '\^\{\}'
assert_workflow_contains "${update_workflow}" 'stable event does not query GitHub Releases' 'repos/.*/releases\?per_page='
assert_workflow_contains "${update_workflow}" 'stable event does not reject draft releases' 'draft.*false'
assert_workflow_contains "${update_workflow}" 'stable event is not gated on the latest published release' 'published_at.*max_by|max_by.*published_at'
assert_workflow_contains "${update_workflow}" 'update runs are not globally serialized for dev' 'group: saola-cli-update-dev'
assert_workflow_contains "${update_workflow}" 'update workflow does not refresh the dev baseline before branching' 'fetch origin dev'
assert_workflow_contains "${update_workflow}" 'update workflow does not rebuild the exact CLI source' 'make -C saola-cli-source'
assert_workflow_contains "${update_workflow}" 'update workflow does not generate release checksums' 'release-build release-checksums'
assert_workflow_contains "${update_workflow}" 'update workflow does not compare the rebuilt amd64 checksum' 'actual_amd64.*CLI_AMD64_SHA256'
assert_workflow_contains "${update_workflow}" 'update workflow does not compare the rebuilt arm64 checksum' 'actual_arm64.*CLI_ARM64_SHA256'
assert_workflow_contains "${update_workflow}" 'update workflow does not validate the lock it writes' 'saola-cli-lock\.sh.*validate'
assert_workflow_contains "${update_workflow}" 'stable update PR label contract is missing' 'automation:saola-cli-stable'
assert_workflow_contains "${update_workflow}" 'update workflow does not use concurrency' '^concurrency:'
assert_workflow_contains "${update_workflow}" 'update workflow does not target dev' '(--base|base:) dev'
assert_workflow_contains "${update_workflow}" 'update workflow does not enable auto-merge' 'pr merge.*--auto.*--squash'
assert_workflow_excludes "${update_workflow}" 'update workflow directly pushes a protected branch' 'git push[^#]*(origin )?(dev|master)([[:space:]]|$)'

[[ -f "${promote_workflow}" ]] || fail 'missing saola-cli promotion workflow'
assert_workflow_contains "${promote_workflow}" 'promotion workflow is not scheduled' '^  schedule:'
assert_workflow_contains "${promote_workflow}" 'promotion workflow is not manually dispatchable' '^  workflow_dispatch:'
assert_workflow_contains "${promote_workflow}" 'promotion does not select the stable automation label' 'automation:saola-cli-stable'
assert_workflow_contains "${promote_workflow}" 'promotion does not verify automation author' 'OPENSAOLA_AUTOMATION_LOGIN|EXPECTED_AUTOMATION_LOGIN'
assert_workflow_contains "${promote_workflow}" 'promotion does not verify deterministic head branch' 'headRefName'
assert_workflow_contains "${promote_workflow}" 'promotion does not enforce lock-only file changes' 'build/saola-cli\.lock'
assert_workflow_contains "${promote_workflow}" 'promotion does not revalidate latest published release' 'published_at.*max_by|max_by.*published_at'
assert_workflow_contains "${promote_workflow}" 'promotion does not revalidate Release checksums' 'SHA256SUMS'
assert_workflow_contains "${promote_workflow}" 'promotion does not use the stable PR merge commit lock' 'mergeCommit\.oid'
assert_workflow_contains "${promote_workflow}" 'promotion does not read the candidate lock at its merge SHA' 'contents/build/saola-cli\.lock\?ref='
assert_workflow_contains "${promote_workflow}" 'promotion does not enforce the default 24 hour soak' 'SOAK_HOURS:.*24'
assert_workflow_contains "${promote_workflow}" 'promotion does not verify CI on the exact merge SHA' 'workflows/ci\.yml/runs.*head_sha'
assert_workflow_contains "${promote_workflow}" 'promotion does not verify Docker on the exact merge SHA' 'workflows/docker\.yml/runs.*head_sha'
assert_workflow_contains "${promote_workflow}" 'promotion does not target master' '(--base|base:) master'
assert_workflow_contains "${promote_workflow}" 'promotion does not enable auto-merge' 'pr merge.*--auto.*--squash'
assert_workflow_contains "${promote_workflow}" 'promotion no-op does not compare the complete validated lock' 'cmp -s.*candidate_lock.*build/saola-cli\.lock'
assert_workflow_excludes "${promote_workflow}" 'promotion resolves a floating CLI revision' '(version|ref|tag)=(latest|snapshot)'
assert_workflow_excludes "${promote_workflow}" 'promotion directly pushes master' 'git push[^#]*(origin )?master([[:space:]]|$)'

assert_workflow_contains "${docker_workflow}" 'Docker workflow does not validate the CLI lock' 'saola-cli-lock\.sh.*validate'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not checkout locked CLI source' 'repository: harmonycloud/saola-cli'
assert_workflow_contains "${docker_workflow}" 'Docker workflow checkout does not use locked CLI commit' 'ref:.*steps\.cli\.outputs\.commit'
assert_workflow_contains "${docker_workflow}" 'Docker workflow checkout path is not isolated' 'path: \.saola-cli-source'
assert_workflow_contains "${docker_workflow}" 'Docker workflow external checkout persists credentials' 'persist-credentials: false'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not use local CLI named context' 'saola-cli=\./\.saola-cli-source'
assert_workflow_excludes "${docker_workflow}" 'Docker workflow still uses remote git context' 'git#'
assert_workflow_contains "${docker_workflow}" 'Docker workflow platforms changed' 'platforms: linux/amd64,linux/arm64'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not emit SBOM attestations' 'sbom: true'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not emit provenance attestations' 'provenance:'
assert_workflow_contains "${docker_workflow}" 'Docker workflow is missing CLI version OCI metadata' 'org\.opensaola\.saola-cli\.version'
assert_workflow_contains "${docker_workflow}" 'Docker workflow is missing CLI commit OCI metadata' 'org\.opensaola\.saola-cli\.revision'

release_doc="${repo_root}/docs/release-process.md"
assert_workflow_contains "${release_doc}" 'release docs omit default-branch workflow bootstrap' 'default branch.*master'
assert_workflow_contains "${release_doc}" 'release docs omit Contents token permissions' 'Contents: read and write'
assert_workflow_contains "${release_doc}" 'release docs omit Pull requests token permissions' 'Pull requests: read and write'
assert_workflow_contains "${release_doc}" 'release docs omit Actions token permissions' 'Actions: read'
assert_workflow_contains "${release_doc}" 'release docs omit label metadata permission requirements' '[Mm]etadata.*label'
assert_workflow_contains "${release_doc}" 'release docs do not describe latest published stable gating' 'latest published.*non-draft release'
assert_workflow_contains "${release_doc}" 'release docs still describe version-only promotion no-op' 'complete five-field lock'

for workflow in "${update_workflow}" "${promote_workflow}"; do
  while IFS= read -r use; do
    [[ "${use}" =~ @([0-9a-f]{40})([[:space:]]|$) ]] || fail "workflow action is not pinned to a full commit: ${use}"
  done < <(grep -E '^[[:space:]]*-?[[:space:]]*uses:' "${workflow}" || true)
done

printf 'PASS: saola-cli lock contract\n'
