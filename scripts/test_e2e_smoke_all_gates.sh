#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

KIND_CLUSTER="${KIND_CLUSTER:-opensaola-e2e}"

if ! command -v kind >/dev/null 2>&1; then
  echo "kind is required for e2e smoke. Please install kind first."
  exit 1
fi
if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required for e2e smoke. Please install kubectl first."
  exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for e2e smoke to build/load the manager image."
  exit 1
fi

GATES=(
  "FG_FINALIZERS"
  "FG_REPLACEPACKAGE_REQUEUE"
  "FG_MO_FILTER_DEPLOYMENT_EVENTS"
  "FG_MO_FILTER_STATUS_EVENTS"
  "FG_MP_SECRET_ENQUEUE"
  "FG_CONTROLLER_CONCURRENCY_LIMITS"
  "FG_MA_STATUS_DEDUP"
  "FG_METRICS_RECONCILE_STEPS"
  "FG_METRICS_RECONCILE_TOTAL"
  "FG_PACKAGE_CACHE"
)

for g in "${GATES[@]}"; do
  export "${g}=true"
done

echo "Enabled gates for local tests:"
printf ' - %s\n' "${GATES[@]}"

echo
echo "== make test-e2e-smoke (KIND_CLUSTER=${KIND_CLUSTER}) =="
make test-e2e-smoke KIND_CLUSTER="${KIND_CLUSTER}"
