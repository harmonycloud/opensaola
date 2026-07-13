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

## 内置 Saola CLI 解析

Saola CLI 独立发布。OpenSaola 不需要下游 dispatch token、自动化 PAT、机器人登录名、label、定时晋级、候选锁或观察窗口。Docker workflow 在镜像构建开始时主动解析最高的已发布正式 SemVer Saola CLI Release，验证 tag、资产、checksum 和可复现重建结果，然后写入严格五字段 lock。

`build/saola-cli-stable.lock` 是唯一提交到仓库的 Saola CLI lock，字段为 `repository`、`version`、`commit`、`channel` 和 `source_date_epoch`。Docker 构建使用固定到完整 commit 的 BuildKit named context，并把 CLI 版本和 revision 写入 OCI labels。官方镜像平台仍严格限定为 `linux/amd64` 和 `linux/arm64`，同时生成 BuildKit provenance 与 SBOM attestations。

不同 ref 的行为刻意区分：

| Git ref | 行为 |
|---------|------|
| `dev` | 解析最新符合条件的正式 Saola CLI Release。如果已提交的 stable lock 缺失或落后，专用同步 job 只用仓库 `GITHUB_TOKEN` 向 `dev` 提交 `build/saola-cli-stable.lock`，显式重新运行更新后 `dev` 上的 `docker.yml`，当前运行不发布镜像。 |
| `master` | 只读解析最新符合条件的正式 Release，但不写仓库。如果已提交的 stable lock 缺失或落后，workflow 失败，必须先让 `dev` 同步 lock 后再向前合并。 |
| `v*` tag | 不解析更新的 CLI。workflow 只验证 tag 所指提交中已有的 stable lock，并从这份不可变依赖记录构建。 |
| Pull Request | 如果已有 committed stable lock，则用它构建；首个 Saola CLI 正式 Release 之前，缺少 lock 的 PR 会执行显式 bootstrap no-op，不发布镜像。 |

首次启用是 fail-closed：在首个 Saola CLI 正式 Release 出现且 `dev` 已同步 `build/saola-cli-stable.lock` 之前，`master` 不能发布镜像，正式 tag 会失败，绝不会回退到浮动 CLI revision。

### 必需的 GitHub 外部配置

不需要跨仓库 secret。唯一写路径是隔离的 `dev` lock 同步 job，使用仓库 `GITHUB_TOKEN`，只授予 `contents: write` 和 `actions: write`，用于提交 stable lock 并显式触发后续 Docker 运行。

仓库管理员仍应在正式依赖该链路前核验常规平台控制：

- 保护 `master`，只允许 PR 合并，并要求相关 CI、Docker 和 Helm checks。
- 允许 Docker workflow 在 `dev` 上推送唯一的 stable-lock 提交；如果 `dev` 保护规则禁止 workflow push，则改用等价的 GitHub App 写路径。
- 保持 release tag 不可变，且只在 `master` 带有最新 stable lock 并通过检查后打正式 tag。
- 回滚到此前已验证的 Saola CLI 版本时使用普通受审 lock 改动；禁止把 lock 改成浮动 ref。

本地验证能证明 workflow 契约和 lock 内容，但无法证明 hosted runner 权限或 branch protection 行为；正式依赖此链路前仍需在 GitHub 上核验这些设置。

## 本地 Helm 部署

从源码目录部署时使用 Makefile 包装命令：

```bash
make helm-deploy
```

如果当前提交正好是 `v*` 发布 tag，包装命令会使用该精确 tag。否则当当前分支是 `dev`、`master` 或 `main` 时，它会部署当前提交镜像 tag（`sha-<shortsha>`）。短生命周期功能分支会回退到 `dev`，因为这些分支默认不会推送自己的 SHA 镜像。

使用 `sha-*` 部署前，需要等待该提交对应的 Docker workflow 完成，确保镜像已经推送到 GHCR。

服务器跟踪 `dev` 分支时，精确提交镜像的一键升级命令是：

```bash
git pull --ff-only && make helm-deploy
```

如果想跟随浮动 `dev` 标签，使用显式滚动目标：

```bash
make helm-deploy-dev
```

该浮动标签目标会设置 `image.tag=dev`、`image.pullPolicy=Always`，并刷新 `podAnnotations.redeployAt`，确保即使 tag 字符串不变，Deployment 也会重新滚动。

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
