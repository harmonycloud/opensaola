#!/usr/bin/env bash
# Compare the same probe on two source trees, alternating order across pairs.
set -euo pipefail
cd "$(dirname "$0")/.."
repo=$PWD
baseline=${PERF_BASE_REF:-}
if [[ -z "$baseline" ]]; then
  baseline=$(git tag --list --sort=-version:refname | awk '/^v[0-9]+\.[0-9]+\.[0-9]+$/ && !found {print; found=1}')
fi
if [[ -z "$baseline" ]] || ! base_sha=$(git rev-parse --verify --end-of-options "$baseline^{commit}"); then
  echo "BLOCKED: fetch a baseline release or set PERF_BASE_REF to an available commit" >&2
  exit 77
fi
pairs=${PERF_PAIRS:-2}
[[ "$pairs" =~ ^[1-9][0-9]*$ ]] || { echo "PERF_PAIRS must be a positive integer" >&2; exit 2; }
if [[ -z "${KUBEBUILDER_ASSETS:-}" ]]; then
  make setup-envtest
  minor=$(go list -m -f '{{.Version}}' k8s.io/api | awk -F'[v.]' '{print "1."$3}')
  KUBEBUILDER_ASSETS=$(bin/setup-envtest use -i "${ENVTEST_K8S_VERSION:-$minor}" --bin-dir "$repo/bin" -p path)
fi
export KUBEBUILDER_ASSETS
if [[ ! -x "$KUBEBUILDER_ASSETS/kube-apiserver" || ! -x "$KUBEBUILDER_ASSETS/etcd" ]]; then
  echo "BLOCKED: envtest requires kube-apiserver and etcd in $KUBEBUILDER_ASSETS" >&2
  exit 77
fi
KUBEBUILDER_ASSETS=$(cd "$KUBEBUILDER_ASSETS" && pwd)
export USE_EXISTING_CLUSTER=false GOWORK=off GOMAXPROCS="${GOMAXPROCS:-4}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/baseline"
git archive -o "$tmp/baseline.tar" "$base_sha"
tar xf "$tmp/baseline.tar" -C "$tmp/baseline"
probe=internal/k8s/managed_resource_perf_test.go
cp "$probe" "$tmp/baseline/$probe"
build() { (cd "$1" && go test -c -tags performance -o "$2" ./internal/k8s); }
build "$tmp/baseline" "$tmp/before"
build "$repo" "$tmp/after"
echo "Baseline $base_sha; candidate working tree at $(git rev-parse HEAD); GOMAXPROCS=$GOMAXPROCS"
"$KUBEBUILDER_ASSETS/kube-apiserver" --version
for ((pair=1; pair<=pairs; pair++)); do
  if ((pair % 2)); then order=(before after); else order=(after before); fi
  for version in "${order[@]}"; do
    source_dir="$repo"
    if [[ "$version" == before ]]; then source_dir="$tmp/baseline"; fi
    echo "Pair $pair/$pairs $version"
    (cd "$source_dir/internal/k8s" && "$tmp/$version" -test.v -test.run '^TestManagedResourcePerformance$' -test.timeout 8m)
  done
done
