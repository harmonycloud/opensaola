# OpenSaola Helm Chart

**English** | [中文](README_zh.md)

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

The chart on the `dev` branch defaults to `ghcr.io/harmonycloud/opensaola:dev`, which is the image tag produced by the GitHub Docker workflow for the `dev` branch.

Or use the Makefile wrapper:

```bash
make helm-deploy
```

## Install A Released OCI Chart

Tagged releases publish this chart to GHCR:

```bash
helm upgrade --install opensaola oci://ghcr.io/harmonycloud/charts/opensaola \
  --version <release-version> \
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

## RBAC Scope

OpenSaola can render and reconcile Kubernetes resources from middleware package templates, including custom resources from package-provided CRDs. The default Helm RBAC therefore includes dynamic resource permissions that match `config/rbac/role.yaml`, so a fresh checkout can be installed or upgraded directly with Helm.

Package catalog Secrets are watched in `config.dataNamespace`. The chart grants Secret metadata patch permissions there so package install/uninstall state can be persisted.

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
