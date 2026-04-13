#!/bin/bash
set -euo pipefail

IMG=${IMG:-opensaola:test}

echo "========================================="
echo "  OpenSaola E2E Full Test"
echo "========================================="

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
echo ""
kubectl logs -n opensaola-system deploy/opensaola-controller-manager --tail=10

echo ""
echo "=== Step 6: Run unit tests ==="
make test

echo ""
echo "========================================="
echo "  ALL TESTS PASSED"
echo "========================================="
