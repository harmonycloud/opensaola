#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
minimal_path="$(mktemp -d)"
trap 'rm -rf "${minimal_path}"' EXIT

for tool in bash git tr date; do
  ln -s "$(command -v "${tool}")" "${minimal_path}/${tool}"
done

make_bin="$(command -v make)"
if output="$(PATH="${minimal_path}" "${make_bin}" -C "${repo_root}" -n helm-deploy 2>&1)"; then
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

echo 'PASS: helm-deploy parses without Go in PATH'
echo 'PASS: helm-deploy defaults to the middleware-operator namespace'
