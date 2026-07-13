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

error_file="$(mktemp)"
trap 'rm -f "${error_file}"' EXIT

assert_template_fails() {
  local description="$1" expected_error="$2"
  shift 2
  if helm template opensaola "${chart}" "$@" >/dev/null 2>"${error_file}"; then
    echo "FAIL: ${description} was accepted" >&2
    exit 1
  fi
  if ! grep -Fqi "${expected_error}" "${error_file}"; then
    cat "${error_file}" >&2
    echo "FAIL: ${description} did not report: ${expected_error}" >&2
    exit 1
  fi
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

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator \
  --set-string global.registry=' ///registry.example.com/// ' \
  --set-string global.repository=' ///platform/// ')"
assert_image "${rendered}" 'registry.example.com/platform/opensaola:dev'
assert_image "${rendered}" 'registry.example.com/platform/kubectl:v1.30.14'

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator \
  --set-string image.registry=' ///manager.example.com/// ' \
  --set-string image.repository=' ///operators/// ' \
  --set-string kubectl.image.registry=' ///tools.example.com/// ' \
  --set-string kubectl.image.repository=' ///platform-tools/// ')"
assert_image "${rendered}" 'manager.example.com/operators/opensaola:dev'
assert_image "${rendered}" 'tools.example.com/platform-tools/kubectl:v1.30.14'

assert_template_fails 'empty manager registry' 'manager image registry is required' \
  --set-string global.registry=/ \
  --set-string image.registry=/ \
  --set-string kubectl.image.registry=tools.example.com
assert_template_fails 'empty manager repository prefix' 'manager image repository prefix is required' \
  --set-string global.repository=/ \
  --set-string image.repository=/ \
  --set-string kubectl.image.repository=platform-tools
assert_template_fails 'empty kubectl registry' 'kubectl image registry is required' \
  --set-string global.registry=/ \
  --set-string image.registry=manager.example.com \
  --set-string kubectl.image.registry=/
assert_template_fails 'empty kubectl repository prefix' 'kubectl image repository prefix is required' \
  --set-string global.repository=/ \
  --set-string image.repository=operators \
  --set-string kubectl.image.repository=/
assert_template_fails 'manager image name override' 'Additional property name is not allowed' \
  --set-string image.name=manager-replacement
assert_template_fails 'kubectl image name override' 'Additional property name is not allowed' \
  --set-string kubectl.image.name=kubectl-replacement

echo 'PASS: Helm image prefixes resolve with fixed image names'
echo 'PASS: Helm image prefixes trim surrounding whitespace and slashes'
echo 'PASS: Helm rejects every empty resolved image prefix'
echo 'PASS: Helm schema rejects manager and kubectl image name overrides'
