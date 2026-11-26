#!/usr/bin/env bash
# Exercise real Helm pruning and Kubernetes namespace garbage collection.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
source "$repo/hack/with-test-kind.sh"

install() {
  helm install "$release" "$1" -n "$ns" --set kubectl.installCRDs=false --set-string fullnameOverride="$ns-controller" \
    --set-string config.dataNamespace="$2" --set config.createDataNamespace="$3" >/dev/null
}
upgrade() {
  helm upgrade "$release" "$chart" -n "$ns" --reuse-values \
    --set-string config.dataNamespace="$1" --set config.createDataNamespace="$2"
}
uninstall() { helm uninstall "$release" -n "$ns"; }
seed() { kubectl -n "$1" create secret generic sample --from-literal=data=catalog-fixture >/dev/null; }
snapshot() {
  kubectl get namespace "$1" -o jsonpath='{.metadata.uid}{" "}{.metadata.deletionTimestamp}{"\n"}' || return 1
  kubectl -n "$1" get secret sample -o jsonpath='{.metadata.uid}{" "}{.data.data}{"\n"}'
}
keep() { kubectl get namespace "$1" -o 'go-template={{index .metadata.annotations "helm.sh/resource-policy"}}'; }
assert_external() {
  local owner
  owner=$(kubectl get ns "$1" -o 'go-template={{with .metadata.annotations}}{{with index . "meta.helm.sh/release-name"}}{{.}}{{end}}{{end}}')
  [[ -z "$owner" ]]
}
assert_protected() {
  [[ $(keep "$1") == keep ]]
  helm get manifest "$release" -n "$ns" | awk -v ns="$1" '
    /^---/{namespace=0; target=0}
    /^kind: Namespace$/{namespace=1}
    namespace && $1 == "name:" && $2 == ns {target=1}
    target && $1 == "helm.sh/resource-policy:" && $2 == "keep" {found=1}
    END {exit !found}'
}

verify_catalog() {
  local tmp ns release chart old
  tmp=$(dirname "$KUBECONFIG")
  ns=operator
  release=catalog-test
  chart="$repo/chart/opensaola"
  old="$tmp/opensaola"
  helm pull oci://ghcr.io/harmonycloud/charts/opensaola --version 1.5.0 --untar --untardir "$tmp"
  kubectl create namespace "$ns"
  install "$old" catalog-old true
  seed catalog-old
  before=$(snapshot catalog-old)
  for target in 'catalog-old false' 'catalog-new true'; do
    read -r name create <<< "$target"
    if upgrade "$name" "$create" >"$tmp/rejected" 2>&1; then
      echo "Unsafe upgrade to $target succeeded" >&2; exit 1
    fi
    grep -F 'data namespace "catalog-old" has no Helm keep policy' "$tmp/rejected"
    [[ $(snapshot catalog-old) == "$before" ]]
    [[ $(helm history "$release" -n "$ns" --max 1 | awk 'END{print $1}') == 1 ]]
  done
  observed=$(kubectl get ns catalog-new --ignore-not-found -o name)
  [[ -z "$observed" ]]
  echo 'PASS: unprotected toggle and rename rejected before a new revision'

  # A second release with the same name in another namespace must not see this
  # old, still-unprotected catalog as its own.
  ns=other-operator
  kubectl create namespace "$ns"
  kubectl create namespace external
  seed external
  external_before=$(snapshot external)
  install "$old" external false
  upgrade external false >/dev/null
  uninstall
  [[ $(snapshot external) == "$external_before" ]]
  assert_external external
  echo 'PASS: external namespace remains external; other release does not block upgrade'
  ns=operator

  upgrade catalog-old true >/dev/null
  assert_protected catalog-old
  [[ $(snapshot catalog-old) == "$before" ]]
  upgrade catalog-old false >/dev/null
  [[ $(snapshot catalog-old) == "$before" ]]
  uninstall
  [[ $(snapshot catalog-old) == "$before" ]]
  echo 'PASS: legacy upgrade records keep; toggle and uninstall retain original data'

  install "$chart" catalog-a true
  assert_protected catalog-a
  seed catalog-a
  a=$(snapshot catalog-a)
  upgrade catalog-b true >/dev/null
  assert_protected catalog-b
  [[ $(snapshot catalog-a) == "$a" ]]
  observed=$(kubectl -n catalog-b get secret sample --ignore-not-found -o name)
  [[ -z "$observed" ]]
  seed catalog-b
  b=$(snapshot catalog-b)
  uninstall
  [[ $(snapshot catalog-a) == "$a" && $(snapshot catalog-b) == "$b" ]]
  echo 'PASS: rename preserves the old catalog; uninstall preserves the currently managed catalog'

  install "$chart" "$ns" true
  seed "$ns"
  same=$(snapshot "$ns")
  upgrade "$ns" false >/dev/null
  uninstall
  [[ $(snapshot "$ns") == "$same" ]]
  assert_external "$ns"
  echo 'PASS: release namespace remains externally managed'
}

with_test_kind verify_catalog
