#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
workflow="${repo_root}/.github/workflows/docker.yml"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -f "${workflow}" ]] || fail "Docker workflow not found: ${workflow}"

metadata_job="$(sed -n '/^  manager-metadata:/,/^  pr-build:/p' "${workflow}")"
pr_job="$(sed -n '/^  pr-build:/,/^  resolve-cli:/p' "${workflow}")"
publish_job="$(sed -n '/^  publish:/,$p' "${workflow}")"

[[ -n "${metadata_job}" ]] || fail 'manager-metadata job is missing'
[[ "${metadata_job}" == *'persist-credentials: false'* ]] || fail 'manager metadata checkout persists credentials'
[[ "${metadata_job}" == *'git rev-parse HEAD'* ]] || fail 'manager metadata is not derived from the checked-out commit'
[[ "${metadata_job}" == *'git show -s --format=%cI'* ]] || fail 'manager build date is not the checked-out commit date'
[[ "${metadata_job}" == *'${GITHUB_SHA}'* ]] || fail 'manager metadata does not verify the checked-out commit against GITHUB_SHA'
[[ "${metadata_job}" == *'version="pr-${PR_NUMBER}"'* ]] || fail 'pull request manager version is not pr-N'
[[ "${metadata_job}" == *'version="${GITHUB_REF_NAME}"'* ]] || fail 'tag and branch manager versions do not preserve the ref name'
[[ "${metadata_job}" == *'^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'* ]] || fail 'manager metadata does not strictly validate release tags'
[[ "${metadata_job}" == *'[[ -n "${value}" ]]'* ]] || fail 'manager metadata does not reject empty output values'
[[ "${metadata_job}" == *'^[0-9a-f]{40}$'* ]] || fail 'manager metadata does not require a full lowercase commit SHA'
[[ "${metadata_job}" == *'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6'* ]] || fail 'manager contract verification does not pin setup-go v6'
[[ "${metadata_job}" == *'VERSION: ${{ steps.metadata.outputs.version }}'* ]] || fail 'manager contract does not verify the resolved version'
[[ "${metadata_job}" == *'GIT_COMMIT: ${{ steps.metadata.outputs.git_commit }}'* ]] || fail 'manager contract does not verify the resolved commit'
[[ "${metadata_job}" == *'BUILD_DATE: ${{ steps.metadata.outputs.build_date }}'* ]] || fail 'manager contract does not verify the resolved build date'
[[ "${metadata_job}" == *'bash hack/manager-version-build_test.sh'* ]] || fail 'manager build contract is not enforced before image builds'
[[ "${metadata_job}" == *'bash hack/manager-version-workflow_test.sh'* ]] || fail 'manager workflow contract is not enforced before image builds'
[[ "${metadata_job}" == *'bash hack/manager-version_test.sh'* ]] || fail 'manager binary identity is not enforced before image builds'

for output in version git_commit build_date; do
  source_value="${output}"
  [[ "${output}" != git_commit ]] || source_value=commit
  expected_export="echo \"${output}=\${${source_value}}\" >>\"\${GITHUB_OUTPUT}\""
  [[ "${metadata_job}" == *"${output}:"* ]] || fail "manager-metadata job output is missing ${output}"
  [[ "${metadata_job}" == *"${expected_export}"* ]] || fail "manager metadata step does not export ${output}"
done

assert_build_contract() {
  local job_name="$1"
  local job="$2"
  local version_output="$3"
  local commit_output="$4"
  local date_output="$5"

  [[ "${job}" == *"VERSION=${version_output}"* ]] || fail "${job_name} is missing VERSION build arg"
  [[ "${job}" == *"GIT_COMMIT=${commit_output}"* ]] || fail "${job_name} is missing GIT_COMMIT build arg"
  [[ "${job}" == *"BUILD_DATE=${date_output}"* ]] || fail "${job_name} is missing BUILD_DATE build arg"
  [[ "${job}" == *'SAOLA_CLI_VERSION='* ]] || fail "${job_name} dropped SAOLA_CLI_VERSION"
  [[ "${job}" == *'SAOLA_CLI_COMMIT='* ]] || fail "${job_name} dropped SAOLA_CLI_COMMIT"
  [[ "${job}" == *'SAOLA_CLI_SOURCE_DATE_EPOCH='* ]] || fail "${job_name} dropped SAOLA_CLI_SOURCE_DATE_EPOCH"
  [[ "${job}" == *"org.opencontainers.image.version=${version_output}"* ]] || fail "${job_name} does not unify the OCI version label"
  [[ "${job}" == *"org.opencontainers.image.revision=${commit_output}"* ]] || fail "${job_name} does not unify the OCI revision label"
  [[ "${job}" == *"org.opencontainers.image.created=${date_output}"* ]] || fail "${job_name} does not unify the OCI created label"
}

[[ "${pr_job}" == *'needs: manager-metadata'* ]] || fail 'pull request build does not depend on validated manager metadata'
[[ "${publish_job}" == *'needs: [manager-metadata, resolve-cli]'* ]] || fail 'publish does not directly depend on manager and CLI metadata validation'
[[ "${publish_job}" == *'- name: Build verification image'* ]] || fail 'publish does not build a loadable verification image'
[[ "${publish_job}" == *'platforms: linux/amd64'* ]] || fail 'publish verification image is not pinned to the runner platform'
[[ "${publish_job}" == *'load: true'* && "${publish_job}" == *'push: false'* ]] || fail 'publish verification image is not local-only'
[[ "${publish_job}" == *'docker run --rm "${IMAGE}" --version'* ]] || fail 'publish does not execute manager --version from the built image'
[[ "${publish_job}" == *'docker image inspect --format'* ]] || fail 'publish does not inspect actual image labels'
for label in version revision created; do
  [[ "${publish_job}" == *"verify_label org.opencontainers.image.${label}"* ]] || fail "publish does not verify the actual OCI ${label} label"
done
verify_line="$(grep -nF -- '      - name: Verify built manager image' <<<"${publish_job}" | cut -d: -f1)"
push_line="$(grep -nF -- '      - name: Build and push' <<<"${publish_job}" | cut -d: -f1)"
[[ -n "${verify_line}" && -n "${push_line}" && "${verify_line}" -lt "${push_line}" ]] || fail 'publish can push before verifying the built image'
assert_build_contract 'pull request build' "${pr_job}" \
  '${{ needs.manager-metadata.outputs.version }}' \
  '${{ needs.manager-metadata.outputs.git_commit }}' \
  '${{ needs.manager-metadata.outputs.build_date }}'
assert_build_contract 'publish build' "${publish_job}" \
  '${{ needs.manager-metadata.outputs.version }}' \
  '${{ needs.manager-metadata.outputs.git_commit }}' \
  '${{ needs.manager-metadata.outputs.build_date }}'

printf 'PASS: manager version workflow contract\n'
