#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
minimal_path="$(mktemp -d)"
trap 'rm -rf "${minimal_path}"' EXIT

for tool in bash git tr date; do
  ln -s "$(command -v "${tool}")" "${minimal_path}/${tool}"
done

make_bin="$(command -v make)"

cat >"${minimal_path}/skopeo" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${NETWORK_LOG:?}"
exit 99
EOF
chmod +x "${minimal_path}/skopeo"

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
  HELM_MANAGER_IMAGE_NAME=manager-replacement \
  HELM_KUBECTL_IMAGE_NAME=kubectl-replacement \
  HELM_IMAGE_TAG=test-tag \
  HELM_SYNC_IMAGE=true 2>&1)"
grep -Fq 'registry.internal/middleware/opensaola' <<<"${internal_output}"
grep -Fq 'registry.internal/middleware/kubectl' <<<"${internal_output}"

normalized_output="$(PATH="${minimal_path}" "${make_bin}" -C "${repo_root}" -n helm-deploy \
  HELM_GLOBAL_IMAGE_REGISTRY=' /global.example.com/// ' \
  HELM_GLOBAL_IMAGE_REPOSITORY=' ///global-prefix/ ' \
  HELM_IMAGE_REGISTRY=' manager.example.com/// ' \
  HELM_IMAGE_REPOSITORY=' ///operators/ ' \
  HELM_KUBECTL_IMAGE_REGISTRY=' tools.example.com/// ' \
  HELM_KUBECTL_IMAGE_REPOSITORY=' ///platform-tools/ ' \
  HELM_INTERNAL_REGISTRY=' registry.internal/// ' \
  HELM_INTERNAL_REPOSITORY=' ///middleware/ ' \
  HELM_IMAGE_TAG=test-tag \
  HELM_SYNC_IMAGE=true 2>&1)"
grep -Fq 'manager.example.com/operators/opensaola:test-tag' <<<"${normalized_output}"
grep -Fq 'tools.example.com/platform-tools/kubectl:v1.30.14' <<<"${normalized_output}"
grep -Fq 'registry.internal/middleware/opensaola:test-tag' <<<"${normalized_output}"
grep -Fq 'registry.internal/middleware/kubectl:v1.30.14' <<<"${normalized_output}"
grep -Fq -- '--set global.registry="registry.internal"' <<<"${normalized_output}"
grep -Fq -- '--set global.repository="middleware"' <<<"${normalized_output}"

normalized_global_output="$(PATH="${minimal_path}" "${make_bin}" -C "${repo_root}" -n helm-deploy \
  HELM_GLOBAL_IMAGE_REGISTRY=' registry.public/// ' \
  HELM_GLOBAL_IMAGE_REPOSITORY=' ///shared/ ' \
  HELM_IMAGE_TAG=test-tag 2>&1)"
grep -Fq 'registry.public/shared/opensaola:test-tag' <<<"${normalized_global_output}"
grep -Fq 'registry.public/shared/kubectl:v1.30.14' <<<"${normalized_global_output}"
grep -Fq -- '--set global.registry="registry.public"' <<<"${normalized_global_output}"
grep -Fq -- '--set global.repository="shared"' <<<"${normalized_global_output}"

normalized_public_override_output="$("${make_bin}" -s -C "${repo_root}" helm-deploy \
  HELM=echo \
  HELM_AUTO_NAMESPACE=false \
  HELM_IMAGE_TAG=test-tag \
  HELM_IMAGE_REGISTRY=' manager.public/// ' \
  HELM_IMAGE_REPOSITORY=' ///operators/ ' \
  HELM_KUBECTL_IMAGE_REGISTRY=' tools.public/// ' \
  HELM_KUBECTL_IMAGE_REPOSITORY=' ///platform-tools/ ' 2>&1)"
grep -Fq -- '--set image.registry=manager.public' <<<"${normalized_public_override_output}"
grep -Fq -- '--set image.repository=operators' <<<"${normalized_public_override_output}"
grep -Fq -- '--set kubectl.image.registry=tools.public' <<<"${normalized_public_override_output}"
grep -Fq -- '--set kubectl.image.repository=platform-tools' <<<"${normalized_public_override_output}"

network_log="${minimal_path}/network.log"
assert_sync_validation() {
  local description="$1" expected_error="$2"
  shift 2
  : >"${network_log}"
  if invalid_sync_output="$(NETWORK_LOG="${network_log}" PATH="${minimal_path}" "${make_bin}" -s -C "${repo_root}" sync-helm-image \
    HELM_IMAGE_TAG=test-tag \
    HELM_SYNC_IMAGE=true \
    "$@" 2>&1)"; then
    printf '%s\n' "${invalid_sync_output}" >&2
    echo "FAIL: slash-only ${description} was accepted" >&2
    exit 1
  fi
  if ! grep -Fq "${expected_error}" <<<"${invalid_sync_output}"; then
    printf '%s\n' "${invalid_sync_output}" >&2
    echo "FAIL: ${description} validation error was not precise" >&2
    exit 1
  fi
  if grep -Fq 'Syncing ' <<<"${invalid_sync_output}" || [[ -s "${network_log}" ]]; then
    printf '%s\n' "${invalid_sync_output}" >&2
    echo "FAIL: ${description} validation ran after an image sync network operation" >&2
    exit 1
  fi
}

assert_sync_validation 'manager source registry' \
  'Manager source image registry is empty after trimming whitespace and slashes.' \
  HELM_IMAGE_REGISTRY=/ \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=middleware
assert_sync_validation 'manager source repository' \
  'Manager source image repository is empty after trimming whitespace and slashes.' \
  HELM_IMAGE_REPOSITORY=/ \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=middleware
assert_sync_validation 'kubectl source registry' \
  'Kubectl source image registry is empty after trimming whitespace and slashes.' \
  HELM_KUBECTL_IMAGE_REGISTRY=/ \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=middleware
assert_sync_validation 'kubectl source repository' \
  'Kubectl source image repository is empty after trimming whitespace and slashes.' \
  HELM_KUBECTL_IMAGE_REPOSITORY=/ \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=middleware
assert_sync_validation 'internal target registry' \
  'Internal target image registry is empty after trimming whitespace and slashes.' \
  HELM_INTERNAL_REGISTRY=/ \
  HELM_INTERNAL_REPOSITORY=middleware
assert_sync_validation 'internal target repository' \
  'Internal target image repository is empty after trimming whitespace and slashes.' \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=/

default_upgrade_output="$("${make_bin}" -s -C "${repo_root}" helm-deploy \
  HELM=echo \
  HELM_AUTO_NAMESPACE=false \
  HELM_KUBECTL_IMAGE_TAG=public-tag \
  HELM_KUBECTL_IMAGE_PULL_POLICY=Always \
  HELM_IMAGE_TAG=test-tag 2>&1)"
if grep -Fq -- '--set image.registry=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set image.repository=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.registry=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.repository=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.tag=' <<<"${default_upgrade_output}" || \
  grep -Fq -- '--set kubectl.image.pullPolicy=' <<<"${default_upgrade_output}"; then
  printf '%s\n' "${default_upgrade_output}" >&2
  echo 'FAIL: public deployment must not override default component image values' >&2
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
  HELM_KUBECTL_IMAGE_TAG=internal-tag \
  HELM_KUBECTL_IMAGE_PULL_POLICY=Always \
  HELM_IMAGE_TAG=test-tag 2>&1)"
grep -Fq -- '--set global.registry=registry.internal' <<<"${internal_upgrade_output}"
grep -Fq -- '--set global.repository=middleware' <<<"${internal_upgrade_output}"
grep -Fq -- '--set kubectl.image.tag=internal-tag' <<<"${internal_upgrade_output}"
grep -Fq -- '--set kubectl.image.pullPolicy=Always' <<<"${internal_upgrade_output}"
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
echo 'PASS: Make image prefixes trim surrounding whitespace and slashes'
echo 'PASS: image sync rejects an empty source before network operations'
echo 'PASS: helm-deploy passes component prefixes only as public overrides'
echo 'PASS: Helm image names cannot be replaced from the make command line'
echo 'PASS: kubectl tag and pull policy are passed only for internal images'
