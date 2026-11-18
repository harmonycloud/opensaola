# 共享资源的写入与 Kubernetes 字段归属

MID 主资源和 MID/MO Configuration 的可变资源首次通过 POST 创建。已有资源执行带 UID 和 resourceVersion 校验的 JSON Patch；单次请求提交业务字段、历史指针与操作编号。Job 采用一次性创建。MO 专属 Deployment/RBAC 自行管理其生命周期。

在正常更新中，OpenSaola 负责落实 Baseline、MID、PreAction 和有效恢复 override 合成的声明。历史决定省略哪些字段时允许删除。API Server 按请求维护 managedFields；外部 Apply 对待删字段拥有归属时，控制器报告冲突，共享父对象的其他子字段可独立更新。通用 Update manager 名称不能还原历史声明。

Kubernetes schema 决定关联键、set 和 atomic 边界。在未描述内部结构的 opaque 子树中，唯一非空 name 的对象列表支持按 name 合并。由原生 schema 定义的 volumeMounts 使用 mountPath 作键，同一个卷可以挂到多个路径。

固定名称的历史 Secret 同时承载成功声明与至多一次待确认操作。已有不可变历史通过下一次正常更新接续，无需客户运行迁移 CLI。暂停恢复继续按暂停前后实际差异采纳。一次性 Action 对目标做条件增量编辑。

升级、故障恢复和回退条件详见[声明字段写入与历史字段删除](resource-intent_zh.md)。
