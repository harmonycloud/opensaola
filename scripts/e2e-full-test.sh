#!/bin/bash
set -euo pipefail

IMG=${IMG:-opensaola:test}
NS=${NS:-e2e-test}

echo "========================================="
echo "  OpenSaola E2E Full Lifecycle Test"
echo "========================================="

# Cleanup on exit
cleanup() {
  echo ""
  echo "=== Cleanup ==="
  kubectl delete namespace $NS --timeout=60s 2>/dev/null || true
  make undeploy IMG=$IMG ignore-not-found=true 2>/dev/null || true
  make uninstall ignore-not-found=true 2>/dev/null || true
  echo "Cleanup complete."
}
trap cleanup EXIT

echo ""
echo "=== Step 1: Build operator image ==="
make docker-build IMG=$IMG

echo ""
echo "=== Step 2: Install CRDs ==="
make install

echo ""
echo "=== Step 3: Deploy operator ==="
make deploy IMG=$IMG

echo ""
echo "=== Step 4: Wait for operator ready ==="
kubectl wait --for=condition=available --timeout=120s \
  deploy/opensaola-controller-manager -n opensaola-system

echo ""
echo "=== Step 5: Verify operator ==="
kubectl get pods -n opensaola-system
kubectl logs -n opensaola-system deploy/opensaola-controller-manager --tail=10

echo ""
echo "=== Step 6: Create test namespace ==="
kubectl create namespace $NS --dry-run=client -o yaml | kubectl apply -f -

echo ""
echo "=== Step 7: Run Ginkgo E2E tests ==="
make test-e2e

echo ""
echo "=== Step 8: Check operator logs for errors ==="
ERROR_COUNT=$(kubectl logs -n opensaola-system deploy/opensaola-controller-manager --tail=200 2>&1 | grep -ci 'error\|panic\|fatal' || true)
echo "Error count in operator logs: $ERROR_COUNT"
if [ "$ERROR_COUNT" -gt 5 ]; then
  echo "WARNING: High error count in operator logs"
  kubectl logs -n opensaola-system deploy/opensaola-controller-manager --tail=200 2>&1 | grep -i 'error\|panic\|fatal' | tail -10
fi

echo ""
echo "========================================="
echo "  ALL E2E TESTS PASSED"
echo "========================================="
