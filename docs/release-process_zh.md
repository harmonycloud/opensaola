# 发布流程

> English version: [release-process.md](release-process.md)

本项目采用接近 GitHub Flow 的分支模型；当旧的小版本需要补丁维护时，再使用轻量级 `release/*` 分支。正式发布产物由不可变的 `v*` Git tag 驱动。

## 分支策略

| 分支 | 用途 | 规则 |
|------|------|------|
| `master` | 稳定主干和默认集成分支 | 受保护，只通过 PR 合并，CI 必须通过 |
| `dev` | 可选的不稳定集成分支 | 可用于测试部署，之后向前合并到 `master` |
| `feature/*`、`fix/*`、`docs/*` | 短生命周期工作分支 | 向 `dev` 或 `master` 提 PR，合并后删除 |
| `release/x.y` | 旧小版本补丁维护 | 只接收 backport 和 hotfix |
| `hotfix/*` | 临时紧急修复分支 | 先合入主干，需要时再 backport 到 `release/x.y` |

源码文件中不要保留分支专属值。尤其是 `chart/opensaola/Chart.yaml`，不能在 `dev` 和 `master` 上分别维护不同内容；发布工作流会从 Git tag 注入正式的 chart version 和 appVersion。

## Helm Chart 策略

`Chart.yaml.version` 是 Helm Chart 包版本，必须是 SemVer，用于标识 chart 包。

`Chart.yaml.appVersion` 是应用版本元数据；当 `image.tag` 为空时，也会作为默认镜像 tag。

源码分支保留开发态默认值：

```yaml
appVersion: "dev"
version: 0.1.3-dev.0
```

正式发布由 tag 生成。以 `v0.1.3` 为例，Helm 发布工作流会打包为：

```bash
helm package chart/opensaola \
  --version "0.1.3" \
  --app-version "v0.1.3"
```

这样 `dev` 和 `master` 合并时不需要为版本字段反复解决冲突，同时正式 chart 仍然有精确版本。

如果只是修复 chart 模板或 values，可以只递增 chart version，应用镜像版本不必同步变化。

## 镜像 Tag 策略

| 镜像 tag | 含义 | 用途 |
|----------|------|------|
| `dev`、`master`、`main` | 浮动分支镜像 | 开发和集成测试 |
| `pr-<number>` | Pull Request 预览 tag | 默认只用于构建校验，不推送 |
| `sha-<shortsha>` | 提交定位 tag | 排障和回滚 |
| `v0.1.3`、`0.1.3`、`0.1` | 正式发布 tag | 稳定安装 |
| `latest` | 最新稳定正式版 | 只由稳定 `v*` tag 生成 |

Helm Chart 默认 `image.pullPolicy` 为 `IfNotPresent`，保证正式版本部署可复现。Makefile 对不可变的 `sha-*` 和 `v*` tag 也使用 `IfNotPresent`；只有显式选择 `dev`、`master`、`main`、`latest` 等浮动 tag 时才切换为 `Always`。

## 本地 Helm 部署

从源码目录部署时使用 Makefile 包装命令：

```bash
make helm-deploy
```

如果当前提交正好是 `v*` 发布 tag，包装命令会使用该精确 tag。否则当当前分支是 `dev`、`master` 或 `main` 时，它会部署当前提交镜像 tag（`sha-<shortsha>`）。短生命周期功能分支会回退到 `dev`，因为这些分支默认不会推送自己的 SHA 镜像。

使用 `sha-*` 部署前，需要等待该提交对应的 Docker workflow 完成，确保镜像已经推送到 GHCR。

测试正式版本或指定提交镜像时可以覆盖：

```bash
HELM_IMAGE_TAG=v0.1.3 make helm-deploy
HELM_IMAGE_TAG=sha-abcdef1 make helm-deploy
```

## 发布检查清单

1. 将计划发布的变更全部合入 `master`。
2. 确认 `master` 上 CI 和 Docker workflow 均已通过。
3. 创建不可变发布 tag：

   ```bash
   git tag v0.1.3
   git push origin v0.1.3
   ```

4. 确认 Docker workflow 已发布匹配的镜像 tag，尤其是 `vX.Y.Z` 和 `sha-<shortsha>`。
5. 确认 Helm Chart Release workflow 已发布匹配的 OCI chart。该 workflow 会注入 `--version X.Y.Z` 和 `--app-version vX.Y.Z`；不要在 `master` 上手工维护另一套不同值。
6. 创建或更新 GitHub Release notes，写清镜像和 chart 引用。
7. 只有进入下一条开发线时才调整源码分支的开发态元数据；`dev` 和 `master` 应尽量保持相同的 `Chart.yaml` 内容。

## 冲突处理规则

从 `dev` 合并到 `master` 时，优先保留“分支中立、由发布自动化注入版本”的实现。不要把冲突解决成某个分支 `appVersion=master`、另一个分支 `appVersion=dev`。分支专属的镜像选择应该放在 CI 或 Makefile values 中，而不是放在 chart 源码元数据里。
