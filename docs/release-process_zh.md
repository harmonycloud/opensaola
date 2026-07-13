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

## 内置 Saola CLI 渠道

`build/saola-cli-dev.lock` 是 dev 镜像输入，`build/saola-cli-stable-candidate.lock` 是可以保存在 `dev` 上等待观察的 stable 候选，`build/saola-cli-stable.lock` 才是 `master` 与正式 tag 唯一允许使用的 CLI 输入。普通 `dev -> master` 合并即使带入 candidate 文件，也不能改变 master 镜像选择的 CLI；只有 promotion workflow 会把精确候选复制为正式 stable lock。Docker workflow 使用固定到所选完整 commit 的 BuildKit named context 构建，并把 CLI 版本和 revision 写入 OCI labels。官方镜像平台仍严格限定为 `linux/amd64` 和 `linux/arm64`，同时生成 BuildKit provenance 与 SBOM attestations。

`saola-cli` 仓库可以发送以下两种不可变事件：

| 事件 | 可接受的锁 | 目标 |
|------|------------|------|
| `saola-cli-dev` | `channel=dev`、`version=dev-<commit 前 12 位>` | 自动合并 PR 到 `dev` |
| `saola-cli-stable` | `channel=stable`、最终版 `vMAJOR.MINOR.PATCH` tag | 更新 `dev` 上的 candidate lock，并添加 `automation:saola-cli-stable` label |

自动 dispatch 和手动 dispatch 都必须提供 `repository`、`channel`、`version`、完整 40 位 `commit`、`source_date_epoch`，以及两个 Linux 产物各自的小写 64 位 SHA-256。更新 workflow 会把 epoch 绑定到 commit 时间戳、重建两个产物；stable 还会把 payload 与 Release 的 `SHA256SUMS` 资产逐项比较，然后才写入严格五字段 lock。该 workflow 永不直接 push `dev` 或 `master`。

stable 自动化只接受同名、非 draft、非 prerelease，且按 `published_at` 当前最新的最终版 GitHub Release；重放旧的合法 tag 也会 fail closed。每小时运行的晋级 workflow 会选择最新一个已合并、带稳定 label 的更新 PR，从该 PR 的精确 merge commit 中读取 `build/saola-cli-stable-candidate.lock`（不会读取之后继续前进的 `dev` HEAD），并从 `mergedAt` 起默认等待 24 小时。只有同一 merge SHA 上的 `CI`、Docker workflow 与具体的 `Build stable candidate` check 都成功后才允许晋级。仅当 `master` 已有逐字节一致的完整 stable lock 时才 no-op；否则只创建写入 `build/saola-cli-stable.lock` 的 PR。master PR check 会拒绝非指定机器人、非确定性 promotion 分支的 stable lock 改动。晋级过程绝不解析 `latest`、snapshot、prerelease 或其他浮动 CLI revision。

首次启用采用显式 fail-closed bootstrap：可以先把 workflow 文件合入默认分支，而此时如果还没有 Saola CLI 正式 Release，master push 不发布镜像、正式 tag 直接失败，也绝不会回退到 dev lock。首个正式 Release 出现后，candidate、精确构建、观察期和 promotion 流程会自动创建 stable lock，不需要人工编辑文件。

### 必需的 GitHub 外部配置

仅合入这些文件不会自动启用整条链路。仓库管理员还必须在 GitHub 外部完成以下配置：

- 发送 `repository_dispatch` 或等待 schedule 之前，必须先把 `saola-cli-update.yml` 和 `saola-cli-promote.yml` 部署到仓库默认分支 `master`；GitHub 只会把这两类事件路由给默认分支上已经存在的 workflow。bootstrap 期间缺少 `build/saola-cli-stable.lock` 会主动禁止 master 镜像发布并阻断 tag，绝不回退到 dev。
- 创建 Actions secret `OPENSAOLA_AUTOMATION_TOKEN`，其值必须是专用机器人 fine-grained PAT。仓库权限至少包括 **Contents: read and write**、**Pull requests: read and write**、**Actions: read**，还需要 **Metadata: read** 以及施加预创建 stable label 的权限（fine-grained PAT 的 label 操作由 Pull requests write 与 Metadata read 覆盖）。缺少该 secret 时 workflow 会 fail closed，并且不会回退到 `GITHUB_TOKEN`，这样机器人 PR 才能正常触发后续 CI。
- 开启 repository auto-merge，并把 `dev`、`master` 都设置成只允许 PR 的保护分支；要求相关 CI 与 Docker checks，且包括自动化机器人在内都禁止直接 push。
- 预先创建固定 label `automation:saola-cli-stable`，并限制其施加权限；晋级 workflow 把它作为 stable 候选边界。
- 设置仓库变量 `OPENSAOLA_AUTOMATION_LOGIN` 为专用机器人 login。candidate PR 的作者与 candidate-only diff 必须匹配；master check 也只允许该机器人从确定性 promotion 分支修改 `build/saola-cli-stable.lock`。
- 定时任务保持默认 24 小时 soak。手动运行可为经授权的事故处置显式指定其他非负时长，但应保留可审计记录。
- 启用前在 workflow 外建立 stable release denylist 或回滚决策流程。需要阻止候选时，应关闭 auto-merge、移除 stable label 或关闭 promotion PR。自动 stable 事件只接受最新 published release；回滚必须通过普通受审 lock-only PR 指向此前已验证的稳定版本，禁止把 lock 改成浮动 ref。

本地验证无法证明 secret、token scope、label 策略、branch protection、auto-merge 或 hosted runner 行为已正确配置；正式依赖此链路前必须在 GitHub 上逐项核验。

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
