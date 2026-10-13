# 渲染期望记录与存量字段删除

本实现把**正常实例更新**作为建立字段管理依据的时机，不要求升级时批量运行 CLI，也不要求中间件包另写删除字段清单。`managedFields` 不再是这些资源后续删除的唯一依据。控制器不会直接修改它；API Server 仍会根据实际请求正常维护 Apply/Update 记录。

## 写入行为

适用于公共 `ManagedResourceWriter` 管理的 MID 主资源、MID/MO Configuration 资源。MO 自身 Deployment/RBAC 的其他写入路径、一次性 Action，以及 Configuration 整资源停用/删除不在此改动范围。

1. 新可变资源仍用 SSA 首次创建，然后保存当前渲染期望。Job 保持一次性 Create；batch/v1 CronJob 走可变资源流程。
2. 没有记录的存量资源，在下一次正常更新时沿用原 SSA 写入并记录本次明确提交的期望。**不会把整个 live 当成历史期望，不会推测已经省略的旧字段。** 此次更新中显式给出的值仍遵循原 Force Apply 行为；不能把它宣传为保护所有未知 atomic 列表子项。
3. 有记录后，按 `B=上次渲染期望、L=当前实际资源、D=本次渲染期望` 做三方比较。使用 API Server 的真实 OpenAPI/SMD 模型确定 map、关联列表、set、atomic 边界，不猜测所有列表都以 `name` 为键。
4. 最终业务变更和新记录指针一起通过带 UID/resourceVersion 检查的 **单次 JSON Patch** 提交。提交前对完整结果做 server dry-run；不会先删 valueFrom、再单独添加 value。
5. unchanged generation 的启动不批量创建记录、不改业务资源、不重放 PreAction。旧 CLI 方案未正式发布，已经移除其回执、完成标记和启动兼容分支；正常更新不依赖 CLI 迁移。

## 字段责任规则

| 情形 | 行为 |
| --- | --- |
| B 有、D 无，L 仍等于 B | 删除已记录字段/key/关联项 |
| B 外的额外字段或列表项 | 保留 |
| B 有、D 无，但 L 被改写 | 保留并报告 |
| 关联项整体被省略，但现场多了子字段 | 保留整个项并报告，避免误删外部子字段 |
| 其他 root Apply manager 明确共同管理 | 删除时保留并报告；不能转换其 API version 时停止该次写入 |
| D 没有改变，L 已变 | 保留 L |
| 普通标量在 D 中明确改值 | 使用本次 D；这是明确更新行为 |
| atomic map/list 的 B→D 与 B→L 同时发生不同改动 | 保留 L 并报告 |
| `resume-policy=apply` 已获暂停状态机允许 | 重放完整 D，保持显式放弃暂停期间改动的原有语义 |

这里的 B 是“我们曾明确提交的期望”，不是“证明没人曾写过相同字段”。历史通用 `manager/Update` 条目本身既不授权删除，也不阻止已记录模板变化。无法识别使用同一个 manager、写入相同值的另一方意图；本实现不声称提供写入者身份认证。

## 记录与故障恢复

- 记录在 owner 命名空间中的不可变 Secret，带 ownerReference；目标资源仅保存 `middleware.cn/resource-intent: <Secret名称>`。渲染值可能含凭据，不能放入公开 annotation 或 Event。Secret 本身仍需按集群 RBAC、备份及静态加密策略保护。
- 每个版本使用唯一 Secret 名称，先准备记录，再原子提交资源字段与指针，成功后删除上一份记录。失败或响应丢失不会覆盖上一份记录。不确定网络结果时保留准备记录，避免服务端稍后提交后指向已删除的 Secret。
- 常规更新每个资源保留一份当前记录；MID/MO 正常删除时，Secret 通过 ownerReference 随 owner GC。后台清理随 leader 启动，并在启动及每 10 分钟扫描带专用标签的记录；默认未引用记录至少保留 30 分钟，直接读取 API，分页 50 条、顺序处理。当前引用记录永不过期。
- 新记录携带单资源版本发布协议与 baseResourceVersion。指针写入始终校验该 UID/RV；初始化冲突后重新准备记录，保留原 Create 默认化证据，但不复用旧 Secret。后台发现目标版本已变化即可回收未引用记录；版本未变化时先以带 UID/RV 校验的 metadata-only JSON Patch 写入根 metadata 的专用 `middleware.cn/resource-intent-gc-fence` label，使延迟提交失效，再删除记录。该 label 不进入期望或业务字段合并、不写入 Pod 模板；使用 label 是为了避免 Deployment 根 annotation 变更导致 generation 增长。完整 fence 会先 dry-run；出现 admission 业务变更、冲突或结果不确定时保留记录。
- 目标已不存在或同名对象 UID 已变化时，后台可以回收绑定旧 UID 的未引用记录；CRD/API 端点不可用不等于目标对象不存在。当前指向记录、身份不符、损坏记录、权限/网络失败、未知发布协议均不会按年龄强删。缺少发布协议信息的记录保留给正常版本替换或 owner GC。清理失败记录脱敏日志，下一轮重试，不阻断正常 reconcile。删除带 Secret UID/RV 条件，避免并发元数据修改后误删。
- 记录绑定 owner UID、资源 UID/GVK/key、Configuration 名称以及合并模型。跨命名空间/集群级资源要求已存在的 owner UID 来源标记；同命名空间的旧资源可用有效 controller ownerReference 证明身份。同名 MCF 重建仍视为相同逻辑来源。
- Secret 缺失/损坏、资源被同名替换、真实合并拓扑变化、API schema 无法获取时，停止推断删除并返回可重试错误，不退回全量 Update。修复记录或 API 可用性后正常重试。需要放弃某一个记录时，应先备份、确认当前期望，再针对该资源移除记录指针，让下一次正常更新重新建立基线；这会失去此前删除依据，不能当作已清理旧残留。
- Action 编辑携带记录指针或清理标记的实际资源时，底层 Apply 保留原 UID/RV。遇到并发冲突应重新读取并重新计算操作，不能只给旧 payload 换一个新 RV，否则可能恢复已删除的旧记录指针。
- schema 的描述、validation-only 变化及无关 Kind 变化不会使记录失效；真正的字段类型/list-key/atomic 变化会。真实集群从 v2 发现切换到 v3 时，暴露的默认值也可能不同并触发保护，不能承诺跨 Kubernetes 版本永不失效。支持 OpenAPI v3；只有旧集群 `/openapi/v3` 根端点也为 404 时才使用真实 OpenAPI v2；新 CRD 的单个 GV 端点尚未发布时返回可重试错误，403/超时不降级猜测。
- 记录采用 zstd 快速压缩，复用单并发编码器/解码器；压缩确实变小时保存为 `intent.json.zst`，否则保存原始 `intent.json`。业务写入和后台清理使用同一解码逻辑，校验压缩帧 checksum，缺失、损坏或同时存在两种编码时失败关闭。
- 不设置应用层记录大小或解压后 JSON 大小上限，不截断记录。新建前先对业务资源做 server dry-run，计算包含 API 默认值的记录，再对真正编码后的 Secret 做 server dry-run；最终写入由 Kubernetes 校验 Secret、请求体和存储限制。Secret 被拒绝时返回 API 错误，已有业务字段和记录指针不变。MCF 是模板，实例参数、渲染展开及原始/有效期望两份内容可能使记录更大，因此 MCF 可发布不等于记录一定能写入；压缩可以减少这种差异，但不绕过 Kubernetes 的限制。非确定性 admission 在 dry-run 与实际请求间仍可能返回不同内容，不能把 dry-run 当成跨请求事务。Secret.stringData 会规范化成 data 后记录。

## API 默认值与剩余边界

API Server 会添加模板没有写的字段，例如 Secret volume 的 defaultMode。额外字段未必是外部操作，也不能一概当成自己的字段。

记录同时区分原始渲染期望与本次 API 请求补全后的有效期望。只有本次提交相对服务端结果新增加的字段，且位于模板明确提供的关联列表项内部、父结构仍由模板提供，才记录为 API 补全字段。不会把整份 live 或此前已有的默认值/外部字段收入基线。后续同一列表项仍存在时携带已记录默认值；整项移除时一起参与三方比较。因此 JSON Patch 新增的 volume 也可以在下一轮删除，不再依赖旧 SSA 归属。

对首次记录之前已经存在、但模板没有写出的默认字段，无法与外部添加可靠区分，仍保留并报告。因而本实现**不能保证所有旧 volume/container 在第一次更新时都消失**。外部后来修改默认值或给列表项添加子字段，同样保护该项。

同样，首次建立记录之前就已经省略的 Nacos auth 字段不会凭空识别为可删。建立记录以后真实发生的开→关变化才有明确差异依据。PreAction 在内存里把 `{}`/`[]` 合并成什么仍遵循原包约定；如果最终渲染没有省略字段，writer 无法把它当成删除意图。本次不改变全局 MergeMap/MergeArray 语义。

`ResourceIntentPreserved` 日志/Event 说明保留原因，只输出脱敏路径；记录中的保留信息是该次操作审计，不等于当前现场必然仍有残留，也不等于整个实例已完全收敛。

## 回退

回退控制器可停止新的三方更新，但不能恢复已经删除的字段；也不能通过直接回放整份 Secret 中的模板覆盖现场。需要恢复具体业务字段时，重新在 MID/包期望中声明它们并验证结果。旧二进制重新使用 SSA 后，可能重新出现 Update/Apply 共享归属；回退、再次升级均需按单实例确认，不能宣称字段管理完全可逆。

本机测试见 `internal/k8s/intent_local_test.go`（integration tag，限定专用 loopback kind context）；纯算法、schema/存储安全和真实 controller 流程分别有单元及 envtest 覆盖。测试结果与已知保留项必须分开报告。
