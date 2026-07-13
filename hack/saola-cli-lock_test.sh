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
  'version=v1.2.3' \
  "commit=${commit}" \
  'channel=stable' \
  'source_date_epoch=1782812176'

"${helper}" validate "${valid_lock}"
[[ "$("${helper}" get "${valid_lock}" repository)" == 'harmonycloud/saola-cli' ]] || fail 'repository extraction failed'
[[ "$("${helper}" get "${valid_lock}" version)" == 'v1.2.3' ]] || fail 'version extraction failed'
[[ "$("${helper}" get "${valid_lock}" commit)" == "${commit}" ]] || fail 'commit extraction failed'
[[ "$("${helper}" get "${valid_lock}" channel)" == 'stable' ]] || fail 'channel extraction failed'
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

dev_channel="${tmp_dir}/dev-channel.lock"
sed 's/^channel=.*/channel=dev/' "${valid_lock}" >"${dev_channel}"
assert_rejected "${dev_channel}"

dev_version="${tmp_dir}/dev-version.lock"
sed 's/^version=.*/version=dev-dfe685bacbeb/' "${valid_lock}" >"${dev_version}"
assert_rejected "${dev_version}"

stable_prerelease="${tmp_dir}/stable-prerelease.lock"
sed 's/^version=.*/version=v1.2.3-rc.1/' "${valid_lock}" >"${stable_prerelease}"
assert_rejected "${stable_prerelease}"

bad_stable_version="${tmp_dir}/bad-stable-version.lock"
sed 's/^version=.*/version=dev-dfe685bacbeb/' "${valid_lock}" >"${bad_stable_version}"
assert_rejected "${bad_stable_version}"

bad_stable_prerelease="${tmp_dir}/bad-stable-prerelease.lock"
sed 's/^version=.*/version=v1.2.3-01/' "${valid_lock}" >"${bad_stable_prerelease}"
assert_rejected "${bad_stable_prerelease}"

bad_stable_build_metadata="${tmp_dir}/bad-stable-build-metadata.lock"
sed 's/^version=.*/version=v1.2.3+build.1/' "${valid_lock}" >"${bad_stable_build_metadata}"
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
dev_lock_file="${repo_root}/build/saola-cli-dev.lock"
stable_lock_file="${repo_root}/build/saola-cli-stable.lock"
stable_candidate_file="${repo_root}/build/saola-cli-stable-candidate.lock"
[[ ! -e "${dev_lock_file}" ]] || fail 'legacy dev lock still exists'
[[ ! -e "${stable_candidate_file}" ]] || fail 'legacy stable candidate lock still exists'
[[ ! -e "${repo_root}/build/saola-cli.lock" ]] || fail 'legacy shared lock still exists'
build_version='v1.2.3'
build_commit='ca8ab049ce73a79b5bd47c29ec91210042689102'
build_epoch='1783933287'
build_lock_file="${tmp_dir}/build-stable.lock"
write_lock "${build_lock_file}" \
  'repository=harmonycloud/saola-cli' \
  "version=${build_version}" \
  "commit=${build_commit}" \
  'channel=stable' \
  "source_date_epoch=${build_epoch}"
"${helper}" validate "${build_lock_file}"
export SAOLA_CLI_LOCK="${build_lock_file}"
grep -Eq '^SAOLA_CLI_LOCK \?= build/saola-cli-stable\.lock$' "${repo_root}/Makefile" || fail 'Makefile does not default to the stable lock'
if grep -Eq '^SAOLA_CLI_CHANNEL[[:space:]]*\?=' "${repo_root}/Makefile"; then
  fail 'Makefile still exposes SAOLA_CLI_CHANNEL'
fi
[[ ! -e "${stable_lock_file}" ]] || {
  "${helper}" validate "${stable_lock_file}"
  [[ "$("${helper}" get "${stable_lock_file}" channel)" = stable ]] || fail 'stable lock channel is not stable'
}
common_dir="$(git -C "${repo_root}" rev-parse --path-format=absolute --git-common-dir)"
case "${common_dir}" in
  */.git/worktrees/*)
    main_git_dir="${common_dir%%/.git/worktrees/*}/.git"
    ;;
  *)
    main_git_dir="${common_dir}"
    ;;
esac
main_repo_root="$(cd "${main_git_dir}/.." && pwd)"
test_source_repo="${SAOLA_CLI_TEST_REPOSITORY:-$(dirname "${main_repo_root}")/saola-cli}"
if ! git -C "${test_source_repo}" cat-file -e "${build_commit}^{commit}" 2>/dev/null; then
  test_source_repo="${tmp_dir}/saola-cli-source"
  git -C "${test_source_repo}" init -q
  git -C "${test_source_repo}" remote add origin https://github.com/harmonycloud/saola-cli.git
  git -C "${test_source_repo}" fetch -q --depth=1 origin "${build_commit}"
fi
docker_build_output="$(TMPDIR="${tmp_dir}" make -s -C "${repo_root}" docker-build CONTAINER_TOOL=echo IMG=controller:test)"
[[ "${docker_build_output}" != *'git#'* ]] || fail 'docker-build still uses an unusable remote git#SHA context'
docker_build_context="$(sed -n 's/.*--build-context saola-cli=\([^ ]*\).*/\1/p' <<<"${docker_build_output}")"
[[ "${docker_build_context}" == /* ]] || fail 'docker-build default context is not an absolute temporary local directory'
[[ ! -e "${docker_build_context}" ]] || fail 'docker-build temporary context was not cleaned after success'
[[ "${docker_build_output}" == *"--build-arg SAOLA_CLI_VERSION=${build_version}"* ]] || fail 'docker-build is missing the locked CLI version'
[[ "${docker_build_output}" == *"--build-arg SAOLA_CLI_COMMIT=${build_commit}"* ]] || fail 'docker-build is missing the locked CLI commit'
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
[[ "${override_output}" == *"--build-arg SAOLA_CLI_VERSION=${build_version}"* ]] || fail 'command line overrode the lock-derived version'
[[ "${override_output}" == *"--build-arg SAOLA_CLI_COMMIT=${build_commit}"* ]] || fail 'command line overrode the lock-derived commit build arg'
[[ "${override_output}" == *"--build-arg SAOLA_CLI_SOURCE_DATE_EPOCH=${build_epoch}"* ]] || fail 'command line overrode the lock-derived source epoch'
[[ "${override_output}" != *'PWNED=true'* ]] || fail 'command line overrode SAOLA_CLI_BUILD_ARGS'

local_context_output="$(make -s -C "${repo_root}" docker-build CONTAINER_TOOL=echo IMG=controller:test SAOLA_CLI_CONTEXT="${test_source_repo}")"
[[ "${local_context_output}" != *"--build-context saola-cli=${test_source_repo}"* ]] || fail 'explicit SAOLA_CLI_CONTEXT was passed through without a clean archive'
local_build_context="$(sed -n 's/.*--build-context saola-cli=\([^ ]*\).*/\1/p' <<<"${local_context_output}")"
[[ "${local_build_context}" == /* && ! -e "${local_build_context}" ]] || fail 'explicit source repo did not use a cleaned temporary archive'
[[ "${local_context_output}" == *"--build-arg SAOLA_CLI_COMMIT=${build_commit}"* ]] || fail 'local context override changed the locked commit metadata'

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

inspected_output="$(EXPECTED_REPOSITORY="${test_source_repo}" EXPECTED_COMMIT="${build_commit}" TMPDIR="${tmp_dir}" \
  make -s -C "${repo_root}" docker-build CONTAINER_TOOL="${inspector}" IMG=controller:test)"
inspected_context="${inspected_output#INSPECTED_CONTEXT=}"
[[ "${inspected_context}" == /* ]] || fail 'inspector did not receive an absolute temporary local context'
[[ ! -e "${inspected_context}" ]] || fail 'inspected docker-build context was not cleaned'

inspected_buildx_output="$(EXPECTED_REPOSITORY="${test_source_repo}" EXPECTED_COMMIT="${build_commit}" TMPDIR="${tmp_dir}" \
  make -s -C "${repo_root}" docker-buildx CONTAINER_TOOL="${inspector}" IMG=controller:test)"
inspected_buildx_context="${inspected_buildx_output#INSPECTED_CONTEXT=}"
[[ ! -e "${inspected_buildx_context}" ]] || fail 'inspected docker-buildx context was not cleaned'

inspected_explicit_output="$(EXPECTED_REPOSITORY="${test_source_repo}" EXPECTED_COMMIT="${build_commit}" TMPDIR="${tmp_dir}" \
  make -s -C "${repo_root}" docker-build CONTAINER_TOOL="${inspector}" IMG=controller:test SAOLA_CLI_CONTEXT="${test_source_repo}")"
inspected_explicit_context="${inspected_explicit_output#INSPECTED_CONTEXT=}"
[[ "${inspected_explicit_context}" == "${tmp_dir}"/opensaola-saola-cli.* ]] || fail 'explicit source repo did not produce a temporary archive context'
[[ ! -e "${inspected_explicit_context}" ]] || fail 'explicit source repo archive was not cleaned'

if failed_output="$(EXPECTED_REPOSITORY="${test_source_repo}" EXPECTED_COMMIT="${build_commit}" FAIL_AFTER_INSPECT=true TMPDIR="${tmp_dir}" \
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
[[ "$(grep -Ec '^ARG TARGETOS$' "${dockerfile}")" -eq 2 ]] || fail 'Dockerfile does not use BuildKit TARGETOS for both builders'
[[ "$(grep -Ec '^ARG TARGETARCH$' "${dockerfile}")" -eq 2 ]] || fail 'Dockerfile does not use BuildKit TARGETARCH for both builders'
if grep -Eq '^ARG TARGET(OS|ARCH)=' "${dockerfile}"; then
  fail 'Dockerfile overrides automatic BuildKit target platform arguments'
fi
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
helm_workflow="${repo_root}/.github/workflows/helm-chart.yml"

[[ ! -e "${update_workflow}" ]] || fail 'legacy saola-cli update workflow still exists'
[[ ! -e "${promote_workflow}" ]] || fail 'legacy saola-cli promotion workflow still exists'

assert_workflow_contains "${docker_workflow}" 'Docker workflow does not validate the CLI lock' 'saola-cli-lock\.sh.*validate'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not isolate pull request builds' '^  pr-build:'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not isolate CLI resolution' '^  resolve-cli:'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not isolate dev lock synchronization' '^  sync-dev-lock:'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not isolate publishing builds' '^  publish:'
pr_build_job="$(sed -n '/^  pr-build:/,/^  resolve-cli:/p' "${docker_workflow}")"
[[ "${pr_build_job}" == *'contents: read'* ]] || fail 'Docker PR job is not contents-read-only'
[[ "${pr_build_job}" != *'packages: write'* ]] || fail 'Docker PR job can write packages'
[[ "${pr_build_job}" == *'persist-credentials: false'* ]] || fail 'Docker PR checkout persists credentials'
[[ "${pr_build_job}" == *'build/saola-cli-stable.lock'* ]] || fail 'Docker PR job does not use the stable lock'
[[ "${pr_build_job}" == *'enabled=false'* ]] || fail 'Docker PR bootstrap without a stable lock is not an explicit no-op'
resolve_job="$(sed -n '/^  resolve-cli:/,/^  sync-dev-lock:/p' "${docker_workflow}")"
[[ "${resolve_job}" == *"github.event_name != 'pull_request'"* ]] || fail 'CLI resolver can run for pull requests'
[[ "${resolve_job}" == *'contents: read'* ]] || fail 'CLI resolver is not contents-read-only'
[[ "${resolve_job}" == *'resolve-latest'* ]] || fail 'branch builds do not resolve the latest stable CLI release'
[[ "${resolve_job}" == *'verify-lock'* ]] || fail 'tag builds do not verify the committed stable lock'
[[ "${resolve_job}" == *'refs/heads/dev|refs/heads/master'* ]] || fail 'branch resolution is not limited to dev and master'
[[ "${resolve_job}" == *'refs/tags/v*)'* ]] || fail 'tag resolution route is missing'
[[ "${resolve_job}" == *'lock_changed=false'* && "${resolve_job}" == *'build_enabled=true'* ]] || fail 'CLI resolver does not expose build control outputs'
for key in repository version commit channel source_date_epoch lock_changed build_enabled; do
  [[ "${resolve_job}" == *"${key}:"* ]] || fail "CLI resolver job output is missing ${key}"
done
sync_job="$(sed -n '/^  sync-dev-lock:/,/^  publish:/p' "${docker_workflow}")"
[[ "${sync_job}" == *'actions: write'* ]] || fail 'dev lock sync cannot dispatch the Docker workflow'
[[ "${sync_job}" == *'contents: write'* ]] || fail 'dev lock sync cannot push the stable lock'
[[ "${sync_job}" != *'packages: write'* ]] || fail 'dev lock sync can write packages'
[[ "${sync_job}" == *"needs.resolve-cli.outputs.lock_changed == 'true'"* ]] || fail 'dev lock sync is not gated on stale resolver output'
[[ "${sync_job}" == *'git fetch origin dev'* ]] || fail 'dev lock sync does not refresh origin/dev'
[[ "${sync_job}" == *'git checkout -B dev origin/dev'* ]] || fail 'dev lock sync does not start from origin/dev'
[[ "${sync_job}" == *'git diff --cached --name-only'* ]] || fail 'dev lock sync does not inspect the staged mutation surface'
[[ "${sync_job}" == *'build/saola-cli-stable.lock'* ]] || fail 'dev lock sync does not update the stable lock'
[[ "${sync_job}" == *'git push origin HEAD:dev'* ]] || fail 'dev lock sync does not push the updated dev ref'
[[ "${sync_job}" == *'gh workflow run docker.yml --ref dev'* ]] || fail 'dev lock sync does not explicitly dispatch the updated dev ref'
publish_job="$(sed -n '/^  publish:/,$p' "${docker_workflow}")"
[[ "${publish_job}" == *'packages: write'* ]] || fail 'Docker publish job cannot write packages'
[[ "${publish_job}" == *'persist-credentials: false'* ]] || fail 'Docker publish checkout persists credentials'
[[ "${publish_job}" == *'needs: resolve-cli'* ]] || fail 'Docker publish job does not depend on the resolver'
[[ "${publish_job}" == *"needs.resolve-cli.outputs.build_enabled == 'true'"* ]] || fail 'Docker publish job can run when the resolver disables it'
for key in repository version commit channel source_date_epoch; do
  [[ "${publish_job}" == *"needs.resolve-cli.outputs.${key}"* ]] || fail "Docker publish does not consume resolver output ${key}"
done
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not select the dedicated stable lock' 'build/saola-cli-stable\.lock'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not checkout locked CLI source' 'repository: harmonycloud/saola-cli'
assert_workflow_contains "${docker_workflow}" 'Docker workflow checkout does not use resolver CLI commit' 'ref:.*needs\.resolve-cli\.outputs\.commit'
assert_workflow_contains "${docker_workflow}" 'Docker workflow checkout path is not isolated' 'path: \.saola-cli-source'
assert_workflow_contains "${docker_workflow}" 'Docker workflow external checkout persists credentials' 'persist-credentials: false'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not use local CLI named context' 'saola-cli=\./\.saola-cli-source'
assert_workflow_excludes "${docker_workflow}" 'Docker workflow still uses remote git context' 'git#'
assert_workflow_contains "${docker_workflow}" 'Docker workflow platforms changed' 'platforms: linux/amd64,linux/arm64'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not emit SBOM attestations' 'sbom: true'
assert_workflow_contains "${docker_workflow}" 'Docker workflow does not emit provenance attestations' 'provenance:'
assert_workflow_contains "${docker_workflow}" 'Docker workflow is missing CLI version OCI metadata' 'org\.opensaola\.saola-cli\.version'
assert_workflow_contains "${docker_workflow}" 'Docker workflow is missing CLI commit OCI metadata' 'org\.opensaola\.saola-cli\.revision'
assert_workflow_contains "${docker_workflow}" 'dev runs are not serialized by ref' 'group:.*github\.ref'
assert_workflow_contains "${docker_workflow}" 'dev runs can cancel an in-flight lock synchronization' 'cancel-in-progress: false'

for forbidden in \
  repository_dispatch \
  'OPENSAOLA_[A-Z_]*(TOKEN|LOGIN)' \
  'saola-cli-stable-candidate' \
  'saola-cli-dev\.lock' \
  'stable-candidate-build' \
  'automation/promote-saola-cli'; do
  assert_workflow_excludes "${docker_workflow}" 'Docker workflow still contains legacy CLI automation' "${forbidden}"
done

assert_workflow_contains "${helm_workflow}" 'Helm tag publishing does not require the promoted stable lock' 'build/saola-cli-stable\.lock'
assert_workflow_contains "${helm_workflow}" 'Helm tag publishing does not validate the promoted stable lock' 'saola-cli-lock\.sh.*validate'
assert_workflow_contains "${helm_workflow}" 'Helm tag publishing does not enforce the stable channel' 'saola-cli-lock\.sh.*get.*channel'

printf 'PASS: saola-cli lock contract\n'
