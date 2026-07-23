#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

manager="${tmp_dir}/manager"
invalid_kubeconfig="${tmp_dir}/missing-kubeconfig"
version="${VERSION:-v1.2.3-test}"
git_commit="${GIT_COMMIT:-0123456789abcdef0123456789abcdef01234567}"
build_date="${BUILD_DATE:-2026-07-14T08:09:10+08:00}"
package="github.com/harmonycloud/opensaola/internal/version"

(
  cd "${repo_root}"
  go build \
    -ldflags "-X ${package}.Version=${version} -X ${package}.GitCommit=${git_commit} -X ${package}.BuildDate=${build_date}" \
    -o "${manager}" \
    ./cmd
)

stdout_file="${tmp_dir}/stdout"
stderr_file="${tmp_dir}/stderr"
KUBECONFIG="${invalid_kubeconfig}" "${manager}" --version >"${stdout_file}" 2>"${stderr_file}"

expected_file="${tmp_dir}/expected"
printf 'Version: %s\nGit Commit: %s\nBuild Date: %s\n' "${version}" "${git_commit}" "${build_date}" >"${expected_file}"
if ! cmp -s "${expected_file}" "${stdout_file}"; then
  printf '%s\n' 'unexpected --version output:' >&2
  diff -u "${expected_file}" "${stdout_file}" >&2 || true
  exit 1
fi

if [[ -s "${stderr_file}" ]]; then
  printf '%s\n' '--version wrote unexpected stderr:' >&2
  cat "${stderr_file}" >&2
  exit 1
fi

echo "manager --version contract passed"
