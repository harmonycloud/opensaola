#!/usr/bin/env bash
# Run only against a fresh, private Kind cluster, never the caller's context.
set -euo pipefail
umask 077
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
mode=${1:?expected redis or kafka}
kind_bin=${KIND_BIN:-kind}
case "$mode" in
    redis) ;;
    kafka)
        : "${KAFKA_PACKAGE_DIR:?set the original Kafka package directory}"
        : "${KAFKA_FIXTURES:?set the directory containing mo.json, mid.json and dependencies.yaml}"
        for path in "$KAFKA_PACKAGE_DIR/metadata.yaml" "$KAFKA_FIXTURES/mo.json" "$KAFKA_FIXTURES/mid.json" "$KAFKA_FIXTURES/dependencies.yaml"; do
            test -s "$path" || { echo "Missing Kafka fixture: $path" >&2; exit 1; }
        done
        command -v "${SAOLA_BIN:-saola}" >/dev/null
        if [[ -n "${KAFKA_PREVIOUS_MANAGER:-}" ]]; then
            [[ "$KAFKA_PREVIOUS_MANAGER" = /* && -x "$KAFKA_PREVIOUS_MANAGER" ]] || { echo "Expected an absolute path to the previous manager" >&2; exit 1; }
        fi
        ;;
    *) echo 'Expected redis or kafka' >&2; exit 2 ;;
esac
tmp=$(mktemp -d "${TMPDIR:-/tmp}/opensaola-fields.XXXXXXXX")
cluster="opensaola-intent-$mode-$(basename "$tmp" | cut -d. -f2 | tr '[:upper:]' '[:lower:]')"
export KUBECONFIG="$tmp/kubeconfig"
manager_pid=
owned=0
cleanup() {
    status=$?
    if [[ -n "$manager_pid" ]]; then
        kill "$manager_pid" 2>/dev/null || true
        wait "$manager_pid" 2>/dev/null || true
    fi
    if [[ "$status" != 0 && -f "$KUBECONFIG" ]]; then
        kubectl --request-timeout=10s get pods -A -o wide >&2 || true
        kubectl --request-timeout=10s get middlewarepackages,mo,mid -A \
            -o 'custom-columns=KIND:.kind,NAME:.metadata.name,STATE:.status.state,REASON:.status.conditions[*].reason' >&2 || true
    fi
    if [[ "$owned" = 1 ]]; then
        "$kind_bin" delete cluster --name "$cluster" || status=1
    fi
    rm -rf "$tmp"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ $("$kind_bin" get clusters) == *"$cluster"* ]]; then
    echo "Cluster name already in use: $cluster" >&2
    exit 1
fi
owned=1
"$kind_bin" create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --image "${KIND_NODE_IMAGE:-kindest/node:v1.34.3}" --wait 180s
[[ $(kubectl config current-context) == "kind-$cluster" ]]
kubectl apply --server-side -f config/crd/bases
kubectl wait --for=condition=Established --timeout=90s -f config/crd/bases

if [[ "$mode" == redis ]]; then
    img=redis:7.2.7-alpine
    docker image inspect "$img" >/dev/null 2>&1 || docker pull "$img"
    bash hack/load-test-kind-image.sh "$cluster" "$img"
    OPENSAOLA_LOCAL_INTENT_KUBECONFIG="$KUBECONFIG" go test -tags integration ./internal/k8s -run '^TestLocalIntentReconcile$' -v -count=1 -timeout 20m
else
    # Optional preloaded OCI/docker archives, one platform per tar. Otherwise
    # kubelet pulls the exact images selected by the package and its manifests.
    for archive in "$KAFKA_FIXTURES"/images/*.tar; do
        [[ -f "$archive" ]] || continue
        docker exec -i "$cluster-control-plane" ctr -n k8s.io images import --local --all-platforms - < "$archive"
    done
    go build -o "$tmp/manager" ./cmd
    # config.Initialize resolves these relative to the manager working directory.
    mkdir -p "$tmp/pkg/config"
    cp pkg/config/config.yaml "$tmp/pkg/config/"
    python3 -c 'import hashlib, sys; print("Candidate manager SHA256", hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$tmp/manager"
    start_manager() {
        (cd "$tmp" && unset opensaola_env && exec "$1" --leader-elect=false --metrics-bind-address=0 --health-probe-bind-address=0) >"$tmp/$2.log" 2>&1 &
        manager_pid=$!
        sleep 1
        kill -0 "$manager_pid" || { echo "The $2 manager exited during startup" >&2; exit 1; }
    }
    start_manager "${KAFKA_PREVIOUS_MANAGER:-$tmp/manager}" initial
    python3 hack/test-kafka-fields.py prepare
    if [[ -n "${KAFKA_PREVIOUS_MANAGER:-}" ]]; then
        kill "$manager_pid"
        wait "$manager_pid"
        start_manager "$tmp/manager" candidate
    fi
    python3 hack/test-kafka-fields.py withdraw
fi
