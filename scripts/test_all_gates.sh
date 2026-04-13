#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

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

echo "Enabled gates:"
printf ' - %s\n' "${GATES[@]}"

echo
echo "== make test =="
make test || true

echo
echo "== make test-race =="
make test-race || true

echo
echo "== make lint =="
make lint || true

echo
echo "== make bench =="
make bench || true

echo
echo "== make test-envtest =="
make test-envtest || true

echo
echo "Completed (note: failures are allowed; see above logs)."
