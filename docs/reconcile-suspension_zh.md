[English](reconcile-suspension.md) | **中文**

# Reconcile 暂停与恢复 Runbook

本手册说明如何临时暂停 Middleware（MID）或 MiddlewareOperator（MO）的下层资源写入，并安全恢复协调。
它适用于维护、人工介入、故障处置、迁移/切换和灾备等场景；灾备只是其中一个使用场景。

## 适用范围与保证

- `merge` 只适用于 MID，自动采纳其**主实际 CR `spec`** 中暂停期间产生的变更。
- **PreAction 边界：**暂停捕获只读取实际 CR，不渲染或执行 PreAction。`merge` 恢复时，期望态渲染会在内存中的 MID 副本上重放 CUE PreAction，使其对主 CR `spec` 的影响与正常主 CR 写入一致；每一步都必须有非空 CUE。没有 CUE、需要执行 CMD、HTTP 等非 CUE 操作的步骤会使 `merge` 失败关闭，渲染过程不会执行这些外部操作或 Kubernetes 写入。应改为纯 CUE 预动作、将结果固化为 MID/Baseline 的显式期望值，或仅在明确接受覆盖暂停期间变更时使用 `apply`。
- 不自动采纳 CR metadata、Configuration、PreAction 或 MiddlewareOperator 的变更；这些资源需要分别回写期望配置。
- 暂停开始时的实际 `spec` 和旧 overrides 保存在 MID 同命名空间的不可变 Secret，复用快速 zstd 压缩（压缩无收益时保存 JSON）。`.status.reconcilePause` 只保存 `snapshotSecret` 引用、目标身份、hash 和时间，不保存完整内容，也不依赖进程内缓存。
- 不设应用层快照大小上限，由 Kubernetes 校验存储后的 Secret 和资源大小。暂停开始时通过 GET 保存实际 `spec`；恢复时只比较该快照与当前实际 `spec`。暂停前已存在且没有变化的默认字段、`null` 和已有漂移不会采纳到 MID。关联列表按 API schema 的键比较，set 按成员比较，schema 声明为 atomic 的值按整体处理。
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
2. `.status.reconcilePause.snapshotSecret` 已存在，且指向同命名空间的有效 Secret；
3. `ReconcileAdoption` 不存在或不是 `False`。

快照捕获失败时不要修改主实际 CR。先修复主 CR 缺失、目标解析失败、Secret 写入失败或 Kubernetes 大小限制问题，再重新检查 MID。

### 3. 执行暂停期间需要保留的主 CR 修改

仅修改主实际 CR 的 `spec`，例如执行 `kubectl edit <primary-kind> <primary-name> -n <namespace>`。请记录这次有意修改；不要依赖该流程来保留 CR metadata 或次级资源的改动。

### 4. 使用 `merge` 恢复（PreAction 仅含 CUE 步骤）

请求 `merge` 前，确认所有已配置的 PreAction 每一步都有非空 CUE。CUE 预动作会在内存中重放，因此可以修改主 CR 的 `spec`；没有 CUE、需要执行 CMD、HTTP 等非 CUE 操作的步骤会使控制器拒绝 `merge`，请按[适用范围与保证](#适用范围与保证)中的替代方案处理。

保留暂停注解，并请求恢复策略：

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=merge --overwrite
```

控制器比较暂停开始时的实际 `spec` 和恢复前的实际 `spec`，将期间新增、修改、删除的差异应用到当前渲染期望，再写入 `spec.reconcileOverrides`。仅在实际发生变化的路径上采纳当前 CR 值；当前 MID 的其他配置保留。暂停期间同一路径也修改了 MID 时，本次 `merge` 以 CR 的期间变更为准；后续显式 MID/Baseline 修改仍按既有规则优先。

下一次主 CR 写入前，会再次核对实际 `spec` 没有变化；若再次修改，重新计算差异。快照 Secret 中的 `previousOverrides` 保存暂停前已有的采纳补丁，重试从它开始计算，避免留下已被撤回的中间修改。

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

成功后控制器会清理 status 中的快照引用、两个恢复注解，并删除本次暂停 Secret。删除失败或进程中断留下的记录由已有后台回收器重试清理（默认每 10 分钟扫描、至少保留 30 分钟）；仍被暂停会话引用的 Secret 不会因时间过长被删除。MID 删除时，Secret 随 controller ownerReference 自动回收。Secret 缺失、身份不符或 hash 异常时，`merge` 保持保护，不会在恢复时重拍快照。`spec.reconcileOverrides` 可以继续存在，用于保存已采纳的暂停期间差异；只有 MID/Baseline 后续在同一路径显式变更期望态时，才会覆盖该路径。

## MID：明确放弃暂停期间改动

仅当确认应重放当前渲染期望态、放弃暂停期间主实际 CR 的修改时，才使用 `apply`：

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=apply --overwrite
```

不要直接删除 `suspend-reconcile`。没有有效恢复策略时，MID 会继续保持写入保护，避免回到旧版本的强制覆盖行为。

## 采纳失败

当 `ReconcileAdoption=False` 时，先查看 `reason` 和 `message`：

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq \
  '.status.conditions[] | select(.type == "ReconcileAdoption")'
```

- `ReconcileSnapshotFailed` 或其他采纳失败：保持暂停，先修复提示的身份、schema、渲染或大小问题，再重试。

旧版本的 `desiredSpec` 快照不能作为实际状态基线，不会被自动重拍。升级前应先结束已有暂停会话；若已升级且只有旧快照，保持暂停，人工确认需要保留的实际差异并写入期望配置后，再选择 `apply`。

不要通过直接删除暂停注解绕过失败的合并。只有明确接受暂停期间改动会被覆盖时，才使用 `apply`。

## MiddlewareOperator（MO）使用普通暂停/恢复语义

MO 暂停会保护其 Configuration、RBAC、Deployment 创建/更新和 Deployment 漂移修复。它不执行主 CR 变更采纳，也不使用 `resume-policy`。

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

## 历史实现对比

2026-10-14 的 `78adfe0` 将暂停恢复切换为实际状态快照和期间变更采纳，`v1.4.0` 已包含该变更。下表的旧实现以其父提交 `d97a165` 为准；当前操作使用上面的步骤。

| 判断项 | 旧实现 | 当前实现 |
|--------|--------|----------|
| 暂停基线 | 暂停时渲染的期望 `spec` | 暂停时读取的实际 CR `spec` |
| 存储与确认 | MID status 内的 `desiredSpec` | `.status.reconcilePause.snapshotSecret` 指向同命名空间的有效不可变 Secret；实际 spec 和暂停前 overrides 保存在 Secret 中 |
| 合并规则 | B 为暂停时期望、L 为恢复前实际、D 为当前期望；L=B 时取 D，D=B 时取 L，L=D 时接受共同值，其余差异路径冲突 | 比较暂停开始与恢复前的实际 spec，仅将期间增删改应用到当前期望 |
| 同路径双方修改 | 最终值不同且均偏离 B 时，报告 `ReconcileAdoptionConflict` 并保持暂停 | 本次 `merge` 采用实际 CR 的期间变更；其他当前期望保留 |
| 暂停前已有默认值与漂移 | 可能因实际与渲染期望不同而被采纳 | 暂停期间未变化就不采纳 |
| 列表与 null | 数组整体比较；观察到的实际 null 视为基线值 | 列表遵循 API schema；未变化 null 不采纳，实际发生的变化按 schema 比较 |
| PreAction | 暂停捕获和恢复合并都需要渲染，均受 CUE 渲染限制 | 暂停捕获不渲染；恢复合并才执行内存 CUE 渲染 |
| 大小限制 | 快照期望、恢复前实际 spec、非空 override 的渲染基线各有 256 KiB 应用限制 | 无这些应用层上限，由 Kubernetes 校验资源大小 |
| 合并重试 | 使用当前 MID overrides，可能带入上轮中间采纳结果 | 从快照中的 `previousOverrides` 重算，排除已撤回的中间修改 |

例如，暂停开始时实际与期望副本数都是 3，期间实际改为 5、MID 期望改为 4：旧实现报告冲突，当前 `merge` 得到 5。恢复后再显式把 MID 期望从 4 改成 6，则新期望 6 生效。

另一个例子是暂停前期望为 3、实际已漂移为 2，且暂停期间没有修改：旧实现可能采纳 2；当前实现没有观察到期间变化，使用当前期望 3。

MID 显式选择 `merge`/`apply`、无策略保持保护、写入前核对实际 spec、MO 独立暂停恢复的规则保持不变。只有旧 `desiredSpec` 快照的会话按[采纳失败](#采纳失败)中的升级处理步骤恢复，不能把旧期望快照转换为实际基线。

## 关联文档

- [技术文档](opensaola-technical.md)
- [故障排查指南](troubleshooting_zh.md)
