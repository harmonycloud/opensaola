# OpenSaola Helm Chart

This chart installs the OpenSaola operator and keeps its CRDs up to date through a Helm hook job.

## Install Or Upgrade From Source

From the repository root:

```bash
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace \
  --wait \
  --timeout 5m
```

Or use the Makefile wrapper:

```bash
make helm-deploy
```

## Install A Released OCI Chart

Tagged releases publish this chart to GHCR:

```bash
helm upgrade --install opensaola oci://ghcr.io/harmonycloud/charts/opensaola \
  --version 1.5.0 \
  --namespace opensaola-system \
  --create-namespace \
  --wait \
  --timeout 5m
```

By default, middleware package Secrets are read from the Helm release namespace. To use a separate data namespace:

```bash
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace \
  --set config.dataNamespace=middleware-operator \
  --set config.createDataNamespace=true \
  --wait \
  --timeout 5m
```

## Verify

```bash
kubectl get pods -n opensaola-system -l app.kubernetes.io/name=opensaola
kubectl get crds | grep middleware.cn
kubectl logs -n opensaola-system -l app.kubernetes.io/name=opensaola -f
```

## Local Checks

```bash
make helm-check
```

This runs `helm lint`, renders the chart, packages it, and verifies that chart CRDs match `config/crd/bases`.
