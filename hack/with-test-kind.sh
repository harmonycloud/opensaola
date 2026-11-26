#!/usr/bin/env bash
# Run one command in a fresh local Kind cluster. The supplied command inherits
# only this run's kubeconfig. A test script can source this file and pass a
# shell function as its command; with_test_kind owns the script's lifetime.
with_test_kind() {
  set -euo pipefail
  [[ $# -gt 0 ]] || { echo 'Usage: with-test-kind.sh COMMAND [ARG...]' >&2; exit 2; }
  umask 077
  local kind_bin=${KIND_BIN:-kind}
  local tmp cluster existing owned=0 child=
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/opensaola-e2e.XXXXXXXXXX")
  cluster="opensaola-e2e-$(basename "$tmp" | cut -d. -f2 | tr '[:upper:]' '[:lower:]')"
  export KUBECONFIG="$tmp/kubeconfig" KIND_CLUSTER="$cluster"

  cleanup_kind() {
    local status=$?
    trap - EXIT
    trap '' INT TERM
    if [[ -n "$child" ]]; then
      kill -TERM -- "-$child" 2>/dev/null || true
      wait "$child" 2>/dev/null || true
    fi
    if (( owned )); then
      if ! "$kind_bin" delete cluster --name "$cluster"; then
        echo "Failed to remove test cluster $cluster (test exit $status)" >&2
        status=1
      fi
    fi
    rm -rf "$tmp"
    exit "$status"
  }
  trap cleanup_kind EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  existing=$("$kind_bin" get clusters)
  if grep -Fxq "$cluster" <<<"$existing"; then
    echo "Test cluster name already exists: $cluster" >&2
    exit 1
  fi
  # Kind can leave a node after a failed create, before writing the kubeconfig.
  local -a create=(create cluster --name "$cluster" --kubeconfig "$KUBECONFIG"
    --image "${KIND_NODE_IMAGE:-kindest/node:v1.34.3}" --wait 180s)
  if [[ -n "${KIND_TEST_CONFIG:-}" ]]; then
    [[ -r "$KIND_TEST_CONFIG" ]] || { echo "Cannot read Kind config: $KIND_TEST_CONFIG" >&2; exit 2; }
    create+=(--config "$KIND_TEST_CONFIG")
  fi
  owned=1
  # Separate process groups let a signal stop the entire test or create command.
  set -m
  "$kind_bin" "${create[@]}" &
  child=$!
  wait "$child"
  child=
  [[ $(kubectl config current-context) == "kind-$cluster" ]] || {
    echo "Unexpected kubeconfig for $cluster" >&2; exit 1
  }
  "$@" <&0 &
  child=$!
  wait "$child"
  child=
  exit 0
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  with_test_kind "$@"
fi
