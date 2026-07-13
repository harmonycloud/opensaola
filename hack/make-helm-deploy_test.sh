#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
minimal_path="$(mktemp -d)"
trap 'rm -rf "${minimal_path}"' EXIT

for tool in bash git tr date; do
  ln -s "$(command -v "${tool}")" "${minimal_path}/${tool}"
done

make_bin="$(command -v make)"
if output="$(PATH="${minimal_path}" "${make_bin}" -C "${repo_root}" -n helm-deploy \
  HELM_IMAGE_TAG=test-tag 2>&1)"; then
  :
else
  printf '%s\n' "${output}" >&2
  echo 'FAIL: helm-deploy dry run must parse without Go in PATH' >&2
  exit 1
fi

if grep -Eiq '(^|[[:space:]])go: (command not found|not found)' <<<"${output}"; then
  printf '%s\n' "${output}" >&2
  echo 'FAIL: helm-deploy unexpectedly invoked Go while parsing the Makefile' >&2
  exit 1
fi

if ! grep -Fq "release_namespace='middleware-operator';" <<<"${output}"; then
  printf '%s\n' "${output}" >&2
  echo 'FAIL: helm-deploy must default to the middleware-operator namespace' >&2
  exit 1
fi

grep -Fq -- '--set global.registry="ghcr.io"' <<<"${output}"
grep -Fq -- '--set global.repository="harmonycloud"' <<<"${output}"
grep -Fq 'ghcr.io/harmonycloud/opensaola:test-tag' <<<"${output}"
grep -Fq 'ghcr.io/harmonycloud/kubectl:v1.30.14' <<<"${output}"

internal_output="$(PATH="${minimal_path}" "${make_bin}" -C "${repo_root}" -n helm-deploy \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=middleware \
  HELM_IMAGE_TAG=test-tag \
  HELM_SYNC_IMAGE=true 2>&1)"
grep -Fq 'registry.internal/middleware/opensaola' <<<"${internal_output}"
grep -Fq 'registry.internal/middleware/kubectl' <<<"${internal_output}"

default_upgrade_output="$("${make_bin}" -s -C "${repo_root}" helm-deploy \
  HELM=echo \
  HELM_AUTO_NAMESPACE=false \
  HELM_IMAGE_TAG=test-tag 2>&1)"
if grep -Fq -- '--set image.registry=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set image.repository=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.registry=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.repository=' <<<"${default_upgrade_output}"; then
  printf '%s\n' "${default_upgrade_output}" >&2
  echo 'FAIL: default deployment must use only the global image prefix' >&2
  exit 1
fi

override_output="$("${make_bin}" -s -C "${repo_root}" helm-deploy \
  HELM=echo \
  HELM_AUTO_NAMESPACE=false \
  HELM_IMAGE_TAG=test-tag \
  HELM_IMAGE_REGISTRY=manager.example.com \
  HELM_IMAGE_REPOSITORY=operators \
  HELM_KUBECTL_IMAGE_REGISTRY=tools.example.com \
  HELM_KUBECTL_IMAGE_REPOSITORY=platform-tools 2>&1)"
grep -Fq -- '--set image.registry=manager.example.com' <<<"${override_output}"
grep -Fq -- '--set image.repository=operators' <<<"${override_output}"
grep -Fq -- '--set kubectl.image.registry=tools.example.com' <<<"${override_output}"
grep -Fq -- '--set kubectl.image.repository=platform-tools' <<<"${override_output}"

internal_upgrade_output="$("${make_bin}" -s -C "${repo_root}" helm-deploy \
  HELM=echo \
  HELM_AUTO_NAMESPACE=false \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=middleware \
  HELM_IMAGE_REGISTRY=manager.example.com \
  HELM_IMAGE_REPOSITORY=operators \
  HELM_KUBECTL_IMAGE_REGISTRY=tools.example.com \
  HELM_KUBECTL_IMAGE_REPOSITORY=platform-tools \
  HELM_IMAGE_TAG=test-tag 2>&1)"
grep -Fq -- '--set global.registry=registry.internal' <<<"${internal_upgrade_output}"
grep -Fq -- '--set global.repository=middleware' <<<"${internal_upgrade_output}"
if grep -Fq -- '--set image.registry=' <<<"${internal_upgrade_output}" || \
  grep -Fq -- '--set image.repository=' <<<"${internal_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.registry=' <<<"${internal_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.repository=' <<<"${internal_upgrade_output}"; then
  printf '%s\n' "${internal_upgrade_output}" >&2
  echo 'FAIL: internal deployment must replace public component overrides' >&2
  exit 1
fi

echo 'PASS: helm-deploy parses without Go in PATH'
echo 'PASS: helm-deploy defaults to the middleware-operator namespace'
echo 'PASS: helm-deploy resolves shared public and internal image prefixes'
echo 'PASS: helm-deploy passes component prefixes only as public overrides'
