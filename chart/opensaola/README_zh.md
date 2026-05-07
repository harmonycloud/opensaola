# OpenSaola Helm 包

[English](README.md) | **中文**

本 Helm 包用于安装 OpenSaola 控制器，并通过 Helm 钩子 Job 保持 CRD 为最新版本。

## 从源码安装或升级

在仓库根目录执行：

```bash
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace \
  --wait \
  --timeout 5m
```

从源码目录部署时，建议优先使用下面的 Makefile 包装命令。如果当前提交正好是 `v*` 发布 tag，它会使用该正式 tag；否则在长期分支（`dev`、`master` 或 `main`）上部署当前提交镜像 tag（`sha-<shortsha>`）。直接使用 Helm 命令部署时，请显式设置 `image.tag` 和 `image.pullPolicy`。

也可以使用 Makefile 封装命令：

```bash
make helm-deploy
```

未设置 `HELM_NAMESPACE` 时，封装命令会通过 `helm list -A` 复用已有 `opensaola` release 所在命名空间；没有已有 release 时才安装到 `opensaola-system`。需要显式指定命名空间时可用 `n=<namespace>`，也可继续用 `HELM_NAMESPACE=<namespace>`。

服务器跟踪 `dev` 分支时，推荐的一键升级方式是：

```bash
git pull --ff-only && make helm-deploy
```

该命令会部署当前检出提交对应的 `ghcr.io/harmonycloud/opensaola:sha-<shortsha>` 镜像。执行前请先等 GitHub 上该提交的 Docker workflow 完成。

如果集群拉取 GHCR 较慢，只需要指定内部 Harbor 地址和 OpenSaola 仓库路径，Makefile 会使用内部镜像升级；默认不会同步镜像：

```bash
git pull --ff-only && \
HELM_INTERNAL_REGISTRY=10.10.102.124:443 \
HELM_INTERNAL_REPOSITORY=middleware/opensaola \
make helm-deploy
```

该模式会沿用默认镜像 tag 规则，不需要手动指定 tag。如果需要在升级前同步 OpenSaola 和 CRD hook Job 使用的 kubectl 镜像，加上 `HELM_SYNC_IMAGE=true`：

```bash
git pull --ff-only && \
HELM_INTERNAL_REGISTRY=10.10.102.124:443 \
HELM_INTERNAL_REPOSITORY=middleware/opensaola \
HELM_SYNC_IMAGE=true \
make helm-deploy
```

如果只需要提前同步镜像，不执行 Helm 升级，可以运行：

```bash
HELM_INTERNAL_REGISTRY=10.10.102.124:443 \
HELM_INTERNAL_REPOSITORY=middleware/opensaola \
make helm-sync-image
```

如果想跟随浮动 `dev` 镜像标签，而不是精确提交镜像标签，执行：

```bash
make helm-deploy-dev
```

该目标会使用 `image.tag=dev`、`image.pullPolicy=Always`，并更新 `podAnnotations.redeployAt` 强制 Deployment 滚动，避免同一个 `dev` 标签字符串不变时 Pod 不重建。

## 安装已发布的 OCI 格式 Helm 包

带标签的发行版本会将 Helm 包发布到 GHCR：

```bash
helm upgrade --install opensaola oci://ghcr.io/harmonycloud/charts/opensaola \
  --version <release-version> \
  --namespace opensaola-system \
  --create-namespace \
  --wait \
  --timeout 5m
```

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

## RBAC 权限范围

OpenSaola 可以根据中间件包模板渲染并协调 Kubernetes 资源，包括包内 CRD 提供的自定义资源。因此 Helm 默认 RBAC 包含与 `config/rbac/role.yaml` 对齐的动态资源权限，确保全新源码检出后可以直接通过 Helm 安装或升级。

包目录 Secret 会在 `config.dataNamespace` 中被监听。Helm 包会在该命名空间授予 Secret 元数据 patch 权限，用于持久化包安装和卸载状态。

## 验证

```bash
kubectl get pods -n opensaola-system -l app.kubernetes.io/name=opensaola
kubectl get crds | grep middleware.cn
kubectl logs -n opensaola-system -l app.kubernetes.io/name=opensaola -f
```

## 本地检查

```bash
make helm-check
```

该命令会执行 `helm lint`、渲染 Helm 包、打包 Helm 包，并校验包内 CRD 与 `config/crd/bases` 保持一致。
