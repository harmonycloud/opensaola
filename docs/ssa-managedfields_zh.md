# SSA 首次创建与 CLI 存量迁移

持续管理的 MID 主资源和 MID/MO Configuration 资源首次创建统一使用
`client.Patch(..., client.Apply, FieldOwner("opensaola"), ForceOwnership)`。
Job 等现有一次性资源保留原创建边界；batch/v1 CronJob 使用可变资源流程。
MO 原生 Deployment/RBAC 的非 SSA 生命周期、Configuration 整资源清理不属于本次改动。

1.1.1 普通 Create 产生的 Update 字段归属不会被后来的 Apply 自动继承。
历史字段集迁移已移到 `saola migrate ssa plan/apply/verify`，控制器不再通过 evidence 注解执行整条 Update union。
CLI 仅迁移有历史依据的字段子集，保留其他 manager 和未确认字段。
操作说明见相邻 saola-cli 仓库的 `docs/ssa-migration_zh.md`。

CLI 在资源上留下 `middleware.cn/ssa-cli-migration`。修复版启动时，在 observedGeneration
提前返回之前纯渲染当前完整期望，对带回执的资源执行普通 SSA。已省略字段按正常 SSA 规则删除，
共同拥有的字段可能保留。不可纯渲染的副作用 PreAction 失败关闭，不擅自执行 CMD/HTTP。
成功 Apply 后写 `middleware.cn/ssa-cli-converged`，包含迁移标识和实际 Apply 字段摘要，
便于 CLI 区分正常 API 默认化与所有权意外丢失。

完成标记 `middleware.cn/ssa-compatible-generation` 同时绑定 owner UID/generation、资源 GVK/key/UID 和 CLI 回执。
快速返回前直读这些资源；同名替换、回执变化、缺少收敛回执会使检查重新进入。
动态 watcher 删除事件校验 MID UID 后通知 owner reconcile，使用当前期望重建，不重放旧 tombstone。

现有资源更新携带 RV；新资源 SSA 是 upsert，GET NotFound 到 Apply 之间没有原生 create-only 原子条件。
本改动未增加准入 API，不能把这个首次创建窗口宣称为完全排除。

测试包括控制器启动/generation/暂停回归，以及独立 Kind 中实际 v1.1.1 Nginx/Caddy 创建→CLI迁移→修复版收敛。
详细迁移引擎测试位于 saola-cli 的 `internal/ssamigrate`。
