# OpenSaola Helm Chart

**English** | [中文](README_zh.md)

This chart installs the OpenSaola operator and keeps its CRDs up to date through a Helm hook job.

## Install Or Upgrade From Source

From the repository root:

```bash
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace
```

For source checkouts, prefer the Makefile wrapper below. It uses an exact `v*` tag when the current commit is a release tag; otherwise long-lived branches (`dev`, `master`, or `main`) deploy the current commit image tag (`sha-<shortsha>`). When using Helm directly, set `image.tag` and `image.pullPolicy` explicitly.

Or use the Makefile wrapper:

```bash
make helm-deploy
```

The Makefile wrapper returns after submitting the Helm release by default. Set `HELM_WAIT=true` when you want Helm to wait for resources to become ready, for example `make helm-deploy HELM_WAIT=true HELM_TIMEOUT=10m`.

When `HELM_NAMESPACE` is not set, the wrapper reuses the namespace of an existing `opensaola` release found by `helm list -A`; otherwise it installs into `opensaola-system`. Use `n=<namespace>` (or `HELM_NAMESPACE=<namespace>`) to choose a namespace explicitly.

For a server that tracks the `dev` branch, the preferred one-command upgrade is:

```bash
git pull --ff-only && make helm-deploy
```

This deploys `ghcr.io/harmonycloud/opensaola:sha-<shortsha>` for the checked-out commit. Wait for the GitHub Docker workflow of that commit to finish before running it.

If the cluster pulls GHCR slowly, set only the internal Harbor registry and OpenSaola repository path. The Makefile deploys the internal image and does not sync images by default:

```bash
git pull --ff-only && \
HELM_INTERNAL_REGISTRY=10.10.102.124:443 \
HELM_INTERNAL_REPOSITORY=middleware/opensaola \
make helm-deploy
```

This keeps the default tag selection, so no manual tag is needed. To sync the OpenSaola image and the kubectl image used by the CRD hook Job before upgrading, add `HELM_SYNC_IMAGE=true`:

```bash
git pull --ff-only && \
HELM_INTERNAL_REGISTRY=10.10.102.124:443 \
HELM_INTERNAL_REPOSITORY=middleware/opensaola \
HELM_SYNC_IMAGE=true \
make helm-deploy
```

To sync images ahead of time without running a Helm upgrade, use:

```bash
HELM_INTERNAL_REGISTRY=10.10.102.124:443 \
HELM_INTERNAL_REPOSITORY=middleware/opensaola \
make helm-sync-image
```

Image sync uses `skopeo copy --all` by default to preserve the multi-architecture manifest. The execution environment must have `skopeo` installed; set `HELM_SYNC_MULTI_ARCH=false` only when a current-platform docker/nerdctl fallback is intended.

If you want to follow the floating `dev` image tag instead of the exact commit tag, run:

```bash
make helm-deploy-dev
```

This uses `image.tag=dev`, `image.pullPolicy=Always`, and updates `podAnnotations.redeployAt` so Kubernetes rolls the Deployment even when the image tag string is unchanged.

## Install A Released OCI Chart

Tagged releases publish this chart to GHCR:

```bash
helm upgrade --install opensaola oci://ghcr.io/harmonycloud/charts/opensaola \
  --version <release-version> \
  --namespace opensaola-system \
  --create-namespace
```

By default, middleware package Secrets are read from the Helm release namespace. To use a separate data namespace:

```bash
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace \
  --set config.dataNamespace=middleware-operator \
  --set config.createDataNamespace=true
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
