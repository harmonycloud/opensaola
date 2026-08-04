[English](reconcile-suspension.md) | **中文**

# Reconcile 暂停与恢复 Runbook

本手册说明如何临时暂停 Middleware（MID）或 MiddlewareOperator（MO）的下层资源写入，并安全恢复协调。
它适用于维护、人工介入、故障处置、迁移/切换和灾备等场景；灾备只是其中一个使用场景。

## 适用范围与保证

- `merge` 只适用于 MID，自动采纳其**主实际 CR `spec`** 中暂停期间产生的无冲突变更。
- **PreAction 边界：**暂停快照和 B/L/D 三方合并会在内存中的 MID 副本上重放仅含 CUE 的 PreAction，因此其对主 CR `spec` 的影响与正常主 CR 写入一致，且不会执行命令、HTTP 操作或 Kubernetes 写入。若任一 PreAction 含非 CUE 步骤（例如 CMD 或 HTTP），该 MID 的 `merge` 会失败关闭；应改为纯 CUE 预动作、将结果固化为 MID/Baseline 的显式期望值，或仅在明确接受覆盖暂停期间变更时使用 `apply`。
- 不自动采纳 CR metadata、Configuration、PreAction 或 MiddlewareOperator 的变更；这些资源需要分别回写期望配置。
- 暂停快照保存在 `.status.reconcilePause`，不依赖进程内缓存。
- 快照和主实际 CR `spec` 输入上限均为 256 KiB。数组按整体比较，显式 JSON `null` 不自动采纳；两者都会失败关闭。
- 若主 CR 被删除重建，或 GVK、名称、命名空间发生变化，恢复会失败关闭，绝不会把补丁套用到另一个对象。

## MID：暂停、修改和合并恢复

### 1. 暂停向下层写入

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/suspend-reconcile=true --overwrite
```

控制器仍会执行 finalizer 删除清理和运行状态同步，但会停止 MID 主 CR 写入、Configuration 写入和已删除 CR 的自愈。

### 2. 确认暂停已安全生效

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq '{
  pause: .status.reconcilePause,
  conditions: [.status.conditions[] | select(.type == "ReconcilePaused" or .type == "ReconcileAdoption")]
}'
```

修改主实际 CR 前必须同时满足：

1. `ReconcilePaused=True`；
2. `.status.reconcilePause` 已存在且包含 `desiredSpec`；
3. `ReconcileAdoption` 不存在或不是 `False`。

快照捕获失败时不要修改主实际 CR。先修复主 CR 缺失、渲染失败或大小超限问题，再重新检查 MID。

### 3. 执行暂停期间需要保留的主 CR 修改

仅修改主实际 CR 的 `spec`，例如执行 `kubectl edit <primary-kind> <primary-name> -n <namespace>`。请记录这次有意修改；不要依赖该流程来保留 CR metadata 或次级资源的改动。

### 4. 使用 `merge` 恢复（PreAction 仅含 CUE 步骤）

请求 `merge` 前，确认所有已配置的 PreAction 都仅含 CUE 步骤。CUE 预动作会在内存中重放，因此可以修改主 CR 的 `spec`；若含 CMD、HTTP 或其他非 CUE 步骤，控制器会安全拒绝 `merge`，请按[适用范围与保证](#适用范围与保证)中的替代方案处理。

保留暂停注解，并请求恢复策略：

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=merge --overwrite
```

控制器会比较暂停时的期望态（B）、恢复前主实际 CR `spec`（L）以及当前渲染期望态（D）。L 中无冲突的差异会写入 `spec.reconcileOverrides`；下一次主 CR 写入前，控制器会再次核对 L 没有变化。若外部系统或人工又修改了资源，控制器会重新合并，而不会用旧值覆盖新值。

### 5. 验证恢复完成

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq '{
  suspend: .metadata.annotations["middleware.cn/suspend-reconcile"],
  resumePolicy: .metadata.annotations["middleware.cn/resume-policy"],
  reconcilePause: .status.reconcilePause,
  conditions: [.status.conditions[] | select(.type == "ReconcilePaused" or .type == "ReconcileAdoption")],
  reconcileOverrides: .spec.reconcileOverrides
}'
```

成功后控制器会清理暂停快照和两个恢复注解。`spec.reconcileOverrides` 可以继续存在，用于保存已采纳的暂停期间差异；只有 MID/Baseline 后续在同一路径显式变更期望态时，才会覆盖该路径。

## MID：明确放弃暂停期间改动

仅当确认应重放当前渲染期望态、放弃暂停期间主实际 CR 的修改时，才使用 `apply`：

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=apply --overwrite
```

不要直接删除 `suspend-reconcile`。没有有效恢复策略时，MID 会继续保持写入保护，避免回到旧版本的强制覆盖行为。

## 冲突或采纳失败

当 `ReconcileAdoption=False` 时，先查看 `reason` 和 `message`：

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq \
  '.status.conditions[] | select(.type == "ReconcileAdoption")'
```

- `ReconcileAdoptionConflict`：暂停期间的实际改动与当前期望态修改了同一路径。先在主 CR 或 MID/Baseline 期望态中确定最终值，再重新设置 `resume-policy=merge`。
- `ReconcileSnapshotFailed` 或其他采纳失败：保持暂停，先修复提示的身份、渲染、大小或 JSON `null` 问题，再重试。

不要通过直接删除暂停注解绕过失败的合并。只有明确接受暂停期间改动会被覆盖时，才使用 `apply`。

## MiddlewareOperator（MO）使用普通暂停/恢复语义

MO 暂停会保护其 Configuration、RBAC、Deployment 创建/更新和 Deployment 漂移修复。它不执行 B/L/D 合并，也不使用 `resume-policy`。

```bash
kubectl annotate mo <mo-name> -n <namespace> \
  middleware.cn/suspend-reconcile=true --overwrite

# 在执行暂停期间的修改前确认 ReconcilePaused=True。
kubectl get mo <mo-name> -n <namespace> -o json | jq \
  '.status.conditions[] | select(.type == "ReconcilePaused")'

# 恢复普通期望态 reconcile。
kubectl annotate mo <mo-name> -n <namespace> \
  middleware.cn/suspend-reconcile-
```

恢复 MO 前，如需保留暂停期间的修改，应先更新它的期望配置。关联 MID 独立管理，必须按上面的 MID 流程分别暂停和恢复。

## 关联文档

- [技术文档](opensaola-technical.md)
- [故障排查指南](troubleshooting_zh.md)
