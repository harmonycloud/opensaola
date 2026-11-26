#!/usr/bin/env bash
# Upgrade an installed official chart, using its normal CRD hook and a real MID.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
source hack/with-test-kind.sh
if [[ -z ${KIND_BIN:-} ]] && ! command -v kind >/dev/null && [[ -x bin/kind ]]; then
  export KIND_BIN="$repo/bin/kind"
fi
blocked() { echo "BLOCKED: $*" >&2; exit 77; }
for tool in "${KIND_BIN:-kind}" kubectl helm docker jq git; do
  command -v "$tool" >/dev/null || blocked "Missing $tool"
done
if [[ -n ${HELM_PREVIOUS_CHART:-} && ! -r $HELM_PREVIOUS_CHART ]]; then
  blocked "Previous chart is unreadable: $HELM_PREVIOUS_CHART"
fi
previous=${HELM_PREVIOUS_VERSION:-$(git tag --list --sort=-version:refname | awk '/^v[0-9]+\.[0-9]+\.[0-9]+$/ && !v {v=substr($0,2)} END {print v}')}
[[ $previous =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Supply HELM_PREVIOUS_VERSION as X.Y.Z' >&2; exit 2; }
revision=$(git rev-parse HEAD)
candidate=${HELM_CANDIDATE_IMAGE:-ghcr.io/harmonycloud/opensaola:sha-${revision:0:7}}
if [[ -z ${HELM_CANDIDATE_IMAGE:-} ]]; then
  if [[ -n $(git status --porcelain -- cmd internal pkg api build Dockerfile go.mod go.sum ':!**/*_test.go') ]]; then
    blocked 'Manager inputs differ from HEAD. Set HELM_CANDIDATE_IMAGE to an image built from the working tree.'
  fi
fi
if ! [[ $candidate =~ ^(.+)/opensaola:([a-zA-Z0-9_.-]+)$ ]]; then
  echo 'HELM_CANDIDATE_IMAGE must include the repository and an explicit opensaola tag' >&2; exit 2
fi
candidate_prefix=${BASH_REMATCH[1]}
candidate_tag=${BASH_REMATCH[2]}
# The chart joins registry and repository. A single nonempty prefix suffices
# when the global prefix has been cleared for the manager.
old_image="ghcr.io/harmonycloud/opensaola:v$previous"
hook_image=ghcr.io/harmonycloud/kubectl:v1.30.14
redis_image=redis:7.2.7-alpine

load_image() {
  local image=$1
  docker image inspect "$image" >/dev/null 2>&1 || docker pull "$image" || blocked "Image is unavailable: $image"
  bash hack/load-test-kind-image.sh "$KIND_CLUSTER" "$image"
}

await_mid() {
  local until_time=$((SECONDS + 180)) filter=$1 current
  while (( SECONDS < until_time )); do
    current=$(kubectl -n upgrade-app get mid upgrade-redis -o json)
    if jq -e "$filter" <<<"$current" >/dev/null; then return 0; fi
    sleep 2
  done
  jq '.status' <<<"$current" >&2
  echo "Timed out waiting for MID: $filter" >&2; return 1
}

uid() { kubectl -n "$1" get "$2" -o jsonpath='{.metadata.uid}'; }
redis() { kubectl -n upgrade-app exec deployment/upgrade-redis -- redis-cli --raw "$@"; }

upgrade_test() {
  local chart=${HELM_PREVIOUS_CHART:-} tmp before_hook before_mid before_deploy before_pvc pod token a b
  tmp=$(dirname "$KUBECONFIG")
  if [[ -z $chart ]]; then
    helm pull oci://ghcr.io/harmonycloud/charts/opensaola --version "$previous" --destination "$tmp" || blocked "Previous OCI chart $previous is unavailable"
    chart="$tmp/opensaola-$previous.tgz"
  fi
  helm show chart "$chart" | grep -Fx "appVersion: v$previous"
  helm show chart "$chart" | grep -Fx "version: $previous"
  for image in "$old_image" "$candidate" "$hook_image" "$redis_image"; do load_image "$image"; done
  if [[ -z ${HELM_CANDIDATE_IMAGE:-} ]]; then
    [[ $(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$candidate") == "$revision" ]]
  fi
  echo "Testing official chart $previous ($old_image) -> working tree chart ($candidate)"
  helm install opensaola "$chart" -n operator --create-namespace --set replicas=1 --wait --timeout 3m
  before_hook=$(uid operator job/opensaola-install-crds)
  [[ $(kubectl -n operator get deploy opensaola -o jsonpath='{.spec.template.spec.containers[0].image}') == "$old_image" ]]
  kubectl create namespace upgrade-app
  kubectl apply -f hack/fixtures/helm-upgrade/workload.yaml
  await_mid '.status.state == "Available" and .status.observedGeneration == .metadata.generation'
  kubectl -n upgrade-app rollout status deploy/upgrade-redis --timeout=150s
  before_mid=$(uid upgrade-app mid/upgrade-redis)
  before_deploy=$(uid upgrade-app deploy/upgrade-redis)
  before_pvc=$(uid upgrade-app pvc/redis-data)
  token="upgrade-$before_mid"
  [[ $(redis SET upgrade-key "$token") == OK && $(redis SAVE) == OK ]]
  echo "PASS: existing MID $before_mid has persistent Redis data on PVC $before_pvc"

  helm upgrade opensaola chart/opensaola -n operator --reuse-values \
    --set-string image.registry="$candidate_prefix" --set-string global.repository= \
    --set-string image.tag="$candidate_tag" --set-string kubectl.image.registry=ghcr.io \
    --set-string kubectl.image.repository=harmonycloud --wait --timeout 3m
  [[ $(kubectl -n operator get deploy opensaola -o jsonpath='{.spec.template.spec.containers[0].image}') == "$candidate" ]]
  [[ $(uid operator job/opensaola-install-crds) != "$before_hook" ]]
  kubectl -n operator wait --for=condition=complete job/opensaola-install-crds --timeout=60s
  for crd in middlewares middlewareoperators; do
    kubectl get crd "$crd.middleware.cn" -o json | jq -e '
      .spec.versions[] | select(.name == "v1") | .schema.openAPIV3Schema.properties.status.properties.pendingConfigurationWrite.properties |
      .id.type == "string" and .resource.properties.resourceUID.type == "string"' >/dev/null
  done
  echo "PASS: upgrade hook ran and API server exposes both Configuration checkpoint schemas"
  kubectl apply -f hack/fixtures/helm-upgrade/configurations.yaml
  kubectl -n upgrade-app patch mid upgrade-redis --type merge -p '{"spec":{"configurations":[{"name":"upgrade-a"},{"name":"upgrade-b"}]}}'
  await_mid '.status.state == "Unavailable" and .status.observedGeneration < .metadata.generation and ([.status.renderedConfigurationResources[].name] == ["upgrade-a"]) and .status.pendingConfigurationWrite.resource.name == "upgrade-b" and (.status.pendingConfigurationWrite.id | length > 0)'
  a=$(uid upgrade-app cm/upgrade-a)
  kubectl -n upgrade-app get mid upgrade-redis -o json | jq -e --arg uid "$a" '.status.renderedConfigurationResources[0].resourceUID == $uid' >/dev/null
  kubectl -n operator scale deploy/opensaola --replicas=0
  kubectl -n operator wait --for=delete pod -l app.kubernetes.io/instance=opensaola,app.kubernetes.io/name=opensaola --timeout=100s
  kubectl create namespace upgrade-repair
  kubectl -n operator scale deploy/opensaola --replicas=1
  kubectl -n operator rollout status deploy/opensaola --timeout=150s
  await_mid '.status.state == "Available" and .status.observedGeneration == .metadata.generation and .status.renderedConfigurationResourcesGeneration == .metadata.generation and .status.pendingConfigurationWrite == null'
  b=$(uid upgrade-repair cm/upgrade-b)
  kubectl -n upgrade-app get mid upgrade-redis -o json | jq -e --arg a "$a" --arg b "$b" '
    [.status.renderedConfigurationResources[].resourceUID] | sort == ([$a,$b] | sort)' >/dev/null
  [[ $(uid upgrade-app cm/upgrade-a) == "$a" && $(uid upgrade-app mid/upgrade-redis) == "$before_mid" ]]
  [[ $(uid upgrade-app deploy/upgrade-redis) == "$before_deploy" && $(uid upgrade-app pvc/redis-data) == "$before_pvc" ]]
  echo "PASS: fresh manager retained A and confirmed both actual ConfigMap UIDs"
  pod=$(kubectl -n upgrade-app get pod -l app=upgrade-redis -o jsonpath='{.items[0].metadata.name}')
  kubectl -n upgrade-app delete pod "$pod" --wait
  kubectl -n upgrade-app rollout status deploy/upgrade-redis --timeout=150s
  [[ $(redis GET upgrade-key) == "$token" ]]
  kubectl -n upgrade-app delete mid upgrade-redis --timeout=150s
  kubectl -n upgrade-app wait --for=delete cm/upgrade-a --timeout=90s
  kubectl -n upgrade-repair wait --for=delete cm/upgrade-b --timeout=90s
  [[ $(uid upgrade-app pvc/redis-data) == "$before_pvc" ]]
  echo "PASS: replaced Redis Pod read old data; deleting MID cleaned both ConfigMaps and retained the original PVC"
}

with_test_kind upgrade_test
