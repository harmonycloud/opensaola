# OpenSaola Helm Chart / OpenSaola Helm 包

This chart installs the OpenSaola operator and keeps its CRDs up to date through a Helm hook job.
本 Helm 包用于安装 OpenSaola 控制器，并通过 Helm 钩子 Job 保持 CRD 为最新版本。

## Install Or Upgrade From Source / 从源码安装或升级

From the repository root:
在仓库根目录执行：

```bash
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace \
  --wait \
  --timeout 5m
```

The chart on the `dev` branch defaults to `ghcr.io/harmonycloud/opensaola:dev`, which is the image tag produced by the GitHub Docker workflow for the `dev` branch.
`dev` 分支上的 Helm 包默认使用 `ghcr.io/harmonycloud/opensaola:dev`，该镜像标签由 GitHub Docker 工作流在 `dev` 分支构建生成。

Or use the Makefile wrapper:
也可以使用 Makefile 封装命令：

```bash
make helm-deploy
```

## Install A Released OCI Chart / 安装已发布的 OCI 格式 Helm 包

Tagged releases publish this chart to GHCR:
带标签的发行版本会将 Helm 包发布到 GHCR：

```bash
helm upgrade --install opensaola oci://ghcr.io/harmonycloud/charts/opensaola \
  --version <release-version> \
  --namespace opensaola-system \
  --create-namespace \
  --wait \
  --timeout 5m
```

By default, middleware package Secrets are read from the Helm release namespace. To use a separate data namespace:
默认情况下，中间件包 Secret 会从 Helm 发布实例所在命名空间读取。如果需要使用独立的数据命名空间：

```bash
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace \
  --set config.dataNamespace=middleware-operator \
  --set config.createDataNamespace=true \
  --wait \
  --timeout 5m
```

## RBAC Scope / RBAC 权限范围

OpenSaola can render and reconcile Kubernetes resources from middleware package templates, including custom resources from package-provided CRDs. The default Helm RBAC therefore includes dynamic resource permissions that match `config/rbac/role.yaml`, so a fresh checkout can be installed or upgraded directly with Helm.
OpenSaola 可以根据中间件包模板渲染并协调 Kubernetes 资源，包括包内 CRD 提供的自定义资源。因此 Helm 默认 RBAC 包含与 `config/rbac/role.yaml` 对齐的动态资源权限，确保全新源码检出后可以直接通过 Helm 安装或升级。

Package catalog Secrets are watched in `config.dataNamespace`. The chart grants Secret metadata patch permissions there so package install/uninstall state can be persisted.
包目录 Secret 会在 `config.dataNamespace` 中被监听。Helm 包会在该命名空间授予 Secret 元数据 patch 权限，用于持久化包安装和卸载状态。

## Verify / 验证

```bash
kubectl get pods -n opensaola-system -l app.kubernetes.io/name=opensaola
kubectl get crds | grep middleware.cn
kubectl logs -n opensaola-system -l app.kubernetes.io/name=opensaola -f
```

## Local Checks / 本地检查

```bash
make helm-check
```

This runs `helm lint`, renders the chart, packages it, and verifies that chart CRDs match `config/crd/bases`.
该命令会执行 `helm lint`、渲染 Helm 包、打包 Helm 包，并校验包内 CRD 与 `config/crd/bases` 保持一致。
