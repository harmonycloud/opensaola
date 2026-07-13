#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="${repo_root}/chart/opensaola"

assert_image() {
  local rendered="$1" expected="$2"
  grep -Fq "image: \"${expected}\"" <<<"${rendered}" || {
    echo "FAIL: missing image ${expected}" >&2
    exit 1
  }
}

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator)"
assert_image "${rendered}" 'ghcr.io/harmonycloud/opensaola:dev'
assert_image "${rendered}" 'ghcr.io/harmonycloud/kubectl:v1.30.14'

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator \
  --set-string global.registry=registry.example.com \
  --set-string global.repository=platform)"
assert_image "${rendered}" 'registry.example.com/platform/opensaola:dev'
assert_image "${rendered}" 'registry.example.com/platform/kubectl:v1.30.14'

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator \
  --set-string image.registry=manager.example.com \
  --set-string image.repository=operators \
  --set-string kubectl.image.registry=tools.example.com \
  --set-string kubectl.image.repository=platform-tools)"
assert_image "${rendered}" 'manager.example.com/operators/opensaola:dev'
assert_image "${rendered}" 'tools.example.com/platform-tools/kubectl:v1.30.14'

error_file="$(mktemp)"
trap 'rm -f "${error_file}"' EXIT
if helm template opensaola "${chart}" --set-string global.repository= \
  --set-string image.repository= \
  --set-string kubectl.image.repository=harmonycloud >/dev/null 2>"${error_file}"; then
  echo 'FAIL: empty manager repository prefix was accepted' >&2
  exit 1
fi
grep -Fq 'manager image repository prefix is required' "${error_file}"
echo 'PASS: Helm image prefixes resolve with fixed image names'
