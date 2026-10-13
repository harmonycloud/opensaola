# SSA 创建与渲染期望更新

持续管理的 MID 主资源和 MID/MO Configuration 可变资源首次创建使用
`client.Patch(..., client.Apply, FieldOwner("opensaola"), ForceOwnership)`。
Job 保持一次性 Create；batch/v1 CronJob 使用可变资源流程。
MO 原生 Deployment/RBAC 的其他生命周期路径、Configuration 整资源清理不属于这次字段删除调整。

1.1.1 普通 Create 产生的 Update 字段归属不会被后来的 Apply 自动继承。
当前实现通过[渲染期望记录与三方更新](resource-intent_zh.md)处理正常配置变化：
存量资源在下一次正常更新建立记录，之后比较上次期望、当前实际资源和本次期望，
使用带 UID/resourceVersion 校验的 JSON Patch 一次提交字段变化和新记录指针。
无法确认的历史省略字段保留并报告，不把 live 或通用 manager 名称当作删除依据。

旧 CLI 迁移方案未正式发布，本实现已经移除迁移回执、收敛回执、generation 完成标记及启动兼容分支。
升级启动不会为 unchanged generation 的已有资源批量写入，也不会重新执行其 PreAction。
正常更新无需运行 CLI 字段归属迁移，不直接改写 managedFields。

新资源 SSA 是 upsert，GET NotFound 到 Apply 之间没有原生 create-only 原子条件。
这次调整没有增加准入 API，不能宣称完全排除首次创建竞争窗口。
