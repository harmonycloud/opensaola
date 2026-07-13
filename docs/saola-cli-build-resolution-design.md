# OpenSaola 构建时解析 Saola CLI 设计

日期：2026-07-13  
状态：已批准，待实现

## 决策

Saola CLI 的正式发布与 OpenSaola 的依赖更新解耦。`saola-cli` 只负责发布带双架构产物和校验信息的 GitHub Release；OpenSaola 在镜像构建链路开始时主动解析最新正式 Saola CLI Release。

不再使用 `OPENSAOLA_DISPATCH_TOKEN`，不再由 `saola-cli` 通过 `repository_dispatch` 通知 OpenSaola，也不使用定时轮询。

## 目标

- Saola CLI 发布不依赖下游仓库的凭据或可用性。
- OpenSaola 的构建入口自动发现最新正式 Saola CLI tag。
- 一次构建只解析一次版本，amd64 和 arm64 必须使用相同的版本、commit 和时间戳。
- OpenSaola 正式 tag 始终使用 tag 对应提交中已经固化的 stable lock，重复构建不漂移。
- 整个流程不需要个人 PAT。

## 非目标

- 不自动消费 Saola CLI prerelease、draft Release 或只有 tag 没有 Release 的版本。
- 不在 OpenSaola 正式 tag 构建中修改 Git 分支、tag 或 lock。
- 不恢复已经移除的 `docs/superpowers/` 跟踪。

## Saola CLI 发布职责

`saola-cli/.github/workflows/release.yml` 保留测试、双架构构建、checksum、SBOM、签名、attestation 和 GitHub Release 发布。

删除以下耦合：

- 构建前的 `OPENSAOLA_DISPATCH_TOKEN` 强制校验。
- 发布后的 `repository_dispatch` 请求。
- 仅为 dispatch 派生的事件类型和输出。

结果是 `v1.0.0` 等正式 tag 可以仅依赖 `saola-cli` 自身的 `GITHUB_TOKEN` 完成发布。

## OpenSaola 解析职责

新增一个可独立测试的解析脚本，由 Docker workflow 的首个 job 调用。解析过程必须：

1. 从 `harmonycloud/saola-cli` 已发布的非 draft、非 prerelease Release 中，按 SemVer 选择最高版本。
2. 验证同名 tag 存在并解析到 Release 声明的完整 commit。
3. 下载 `SHA256SUMS`、`saola-linux-amd64` 和 `saola-linux-arm64`。
4. 校验两个二进制与 `SHA256SUMS` 一致。
5. 根据 tag commit 的 committer 时间生成 `source_date_epoch`。
6. 从精确 commit 重建两个架构，并比较重建 checksum 与 Release checksum。
7. 输出唯一的 lock 内容：repository、version、commit、channel 和 source_date_epoch。

任何一步失败都不得继续构建镜像。

## 分支与正式 tag 行为

### dev

`dev` 镜像工作流每次运行时解析最新正式 Saola CLI Release。

- 如果 `build/saola-cli-stable.lock` 已匹配，直接进入镜像构建。
- 如果 lock 缺失或落后，workflow 使用当前仓库的 `GITHUB_TOKEN` 仅提交该 lock 到 `dev`，随后通过 `workflow_dispatch` 对更新后的 `dev` 重新触发 Docker workflow；当前运行不推镜像。
- 重新触发的运行必须再次验证 Release 和 lock，然后才构建镜像。

这样不需要 PAT。由 `GITHUB_TOKEN` 产生的普通 push 不会递归触发 workflow，因此显式使用 `workflow_dispatch` 完成后续构建。

### master

`master` 构建只读。它解析最新正式 Saola CLI Release，并要求 `build/saola-cli-stable.lock` 已与解析结果一致。

如果不一致，构建失败并要求先让 `dev` 完成 lock 同步，再合并 `dev`。workflow 不直接写 `master`。

### v* 正式 tag

正式 tag 构建只使用 tag 所指提交中的 `build/saola-cli-stable.lock`，并重新验证该 lock 对应的 Release、tag commit 和双架构 checksum。

解析 job 的输出作为单一数据源传递给所有架构构建。正式 tag 构建禁止动态替换为更新版本，因此同一个 OpenSaola tag 重跑时不会嵌入不同 Saola CLI。

## 权限边界

- Saola CLI Release workflow：`contents: write`、attestation 和 OIDC 所需权限，不再需要跨仓库 Secret。
- OpenSaola Docker workflow：默认只读；仅 `dev` lock 同步 job 获得 `contents: write` 和触发本仓库 workflow 所需权限。
- `master` 和 `v*` 路径不执行 Git 写操作。
- 如果未来为 `dev` 启用禁止机器人直推的保护规则，workflow 应失败关闭；届时改用 GitHub App，而不是恢复个人 PAT。

## 被替代的旧流程

实现完成后删除或停用：

- `.github/workflows/saola-cli-update.yml`
- `.github/workflows/saola-cli-promote.yml`
- stable candidate PR、24 小时 promotion 和相关 automation label/login/token 契约
- `build/saola-cli-dev.lock` 与 `build/saola-cli-stable-candidate.lock`

保留 `build/saola-cli-stable.lock`，它是正式 OpenSaola tag 的可复现依赖记录。

## 验证要求

### Saola CLI

- 缺少 `OPENSAOLA_DISPATCH_TOKEN` 时，main 和 tag Release workflow 仍可执行。
- `make test` 与 release metadata contract 通过。
- 正式 Release 仍包含双架构二进制、`SHA256SUMS`、SBOM、签名和 attestation。

### OpenSaola

- 解析脚本覆盖：无 Release、draft/prerelease、非法 SemVer、tag/commit 不一致、缺失资产、checksum 不一致和重建不一致。
- Docker workflow 覆盖：dev lock 相同、dev lock 更新并重新触发、master lock 落后、正式 tag lock 固定。
- `hack/saola-cli-lock_test.sh` 在隔离 worktree 中通过。
- `make test`、`make helm-check` 和 workflow 语法检查通过。
- amd64 与 arm64 镜像内的 `saola version` 必须报告相同版本和 commit，且二进制架构正确。

## 本次发布顺序

1. 先在 `saola-cli` 落地发布解耦改造并验证。
2. 发布并验收 `saola-cli v1.0.0`。
3. 根据已发布的 `v1.0.0` 生成并验证 OpenSaola stable lock。
4. 在 OpenSaola `dev` 落地主动解析改造和 stable lock。
5. 合并到 `master`，验证正式路径只读且 lock 固定。
6. 发布 OpenSaola `v1.1.0`，验收双架构镜像和 Helm Chart。
