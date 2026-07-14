#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
workflow="${repo_root}/.github/workflows/cleanup-images.yml"
retention_action='snok/container-retention-policy@d3bdcf5ce9b05f685154e4a16c39233b245e3d53 # v3.1.0'

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

[[ -f "${workflow}" ]] || fail "cleanup workflow not found: ${workflow}"

action_count="$(grep -Fxc "        uses: ${retention_action}" "${workflow}")"
[[ "${action_count}" == 2 ]] || fail 'cleanup steps must both use the token-compatible pinned v3.1.0 action'

if grep -Eq 'uses: snok/container-retention-policy@(v|main|master)' "${workflow}"; then
  fail 'cleanup workflow must pin the retention action by immutable commit SHA'
fi

grep -Eq '^  packages: write$' "${workflow}" || fail 'cleanup workflow is missing packages: write permission'
[[ "$(grep -Fxc '          token: ${{ secrets.GITHUB_TOKEN }}' "${workflow}")" == 2 ]] || fail 'cleanup steps must use the repository GITHUB_TOKEN'
[[ "$(grep -Fxc '          image-names: opensaola' "${workflow}")" == 2 ]] || fail 'cleanup steps must target the exact opensaola package name'

printf 'PASS: cleanup image workflow contract\n'
