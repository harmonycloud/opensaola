# 声明字段写入与历史字段删除

本实现把**正常实例更新**作为建立字段管理依据的时机，不要求升级时批量运行 CLI，也不要求中间件包另写删除字段清单。`managedFields` 不再是这些资源后续删除的唯一依据。控制器不会直接修改它；API Server 仍会根据实际请求正常维护 Apply/Update 记录。

## 写入行为

适用于公共 `ManagedResourceWriter` 管理的 MID 主资源、MID/MO Configuration 资源。MO 自身 Deployment/RBAC 的其他写入路径、以及 Configuration 整资源停用/删除各自处理对象生命周期。一次性 Action 的编辑方式见下文。

1. 新可变资源通过 POST 创建，然后用本次创建响应建立历史。遇到同名资源并发创建，重新读取并核实身份。POST 响应丢失后，写入器直接回读并核对本次请求独有的操作编号。匹配时可为本次明确声明建立历史。只有真实的 POST 响应能提供首次创建的默认化证据。如果进程在保存历史前终止，新的进程无法凭现场还原用户已经撤回的旧声明。Job 保持一次性 Create；batch/v1 CronJob 走可变资源流程。
2. 没有记录、已属于当前 owner 的存量资源，在下一次正常更新时通过带 UID/resourceVersion 校验的 JSON Patch 写入当前明确声明的字段，并建立记录。**不会把整个 live 当成历史声明，不会推测已经省略的旧字段。** 符合下文条件的历史 Configuration 接管采用相同字段规则，不沿用旧 owner 的删除基线。
3. `D` 是 Baseline、MID、PreAction 和有效恢复 override 合成后的本次最终声明；`L` 是当前实际资源；`B` 是历史记录。**写入以 D 为准，B 只用于判断哪些省略字段可以安全删除。** D 仍声明的值，即使相对 B 没变，也必须落实。API 默认化补全的字段不等于用户明确声明。
4. 使用 API Server 的真实 OpenAPI/SMD 模型确定 map、关联列表、set、atomic 边界。只有 schema 未描述结构的 opaque 子树中，元素全部带唯一非空 `name` 的对象列表才递归按 name 合并；显式 map/set/list-key、已知类型的默认 atomic 列表仍遵循 schema。带 name 但重名、缺名或混合元素的模糊列表返回错误，不按位置猜测。
5. 已有资源的业务变更、记录指针和本次操作编号一起通过带 UID/resourceVersion 检查的 **单次 JSON Patch** 提交。提交前对完整结果做 server dry-run，校验声明字段和计划删除路径；提交后再次校验实际响应。不会先删 valueFrom、再单独添加 value，也不会把“API 请求成功但声明被丢弃”报告为更新成功。
6. unchanged generation 的启动不批量创建记录、不重放 PreAction。主资源被删除时单独执行下述重建流程；现存资源不因此进行全字段漂移纠正。正常更新不依赖 CLI 迁移。

## 字段责任规则

| 情形 | 行为 |
| --- | --- |
| B 有、D 无，L 仍等于 B | 删除已记录字段/key/关联项 |
| B 外的额外字段或列表项 | 保留，不作为删除冲突 |
| B 有、D 无，L 已经缺失 | 仍验证这次 API 响应没有重新插入该字段 |
| B 有、D 无，但 L 被改写 | 拒绝整个目标资源的本轮写入，保留原历史并报冲突 |
| 关联项整体被省略，但现场多了子字段 | 递归删除已记录且未被改写的子字段，保留外部子字段和关联键；只剩关联键时删除整个项 |
| 其他 root Apply manager 明确共同管理待删除字段 | 拒绝整个目标资源的本轮写入；不能解释其 API version 时同样停止 |
| D 仍明确声明，L 不符合 D | 落实 D，无须先判断 D 相对 B 是否变化 |
| opaque 的唯一 name 列表 | 按 name 递归落实已声明子字段，保留未涉及的其他成员/子字段 |
| D 明确声明 schema atomic map/list | 按整个值落实 D，atomic 内部不承诺逐字段保留 |
| D 明确写 `null` | 这是显式值，不当成未声明；原生 API 的合法规范化另按类型处理 |
| `resume-policy=merge` 已获暂停状态机允许 | 仍比较暂停前后实际值；先应用有效 override，再按最终 D 写入 |
| `resume-policy=apply` 已获暂停状态机允许 | 使用当前 D，保持显式放弃暂停期间改动的原有语义 |

这里的 B 是“我们曾明确提交的期望”，不是“证明没人曾写过相同字段”。历史通用 `manager/Update` 条目本身既不授权删除，也不阻止已记录模板变化。无法识别使用同一个 manager、写入相同值的另一方意图；本实现不声称提供写入者身份认证。

例如 Kafka 的 opaque Pod 模板中，D 声明 `containers[name=broker].volumeMounts[name=log].mountPath=/root/logs`，L 的同一容器没有该挂载：本轮会补上它，即使 B 已经记录过完全相同的值。L 中 D 未涉及的其他容器、挂载和环境变量保留。若 D 中某个父节点本身明确为 `null`，该节点下的外部字段不能再算“未涉及”；不能把 YAML 的 `metadata:` 当成省略 metadata。

例如随后从 MID 同时省略整个 `containers` 和 `volumes`，历史 broker 只包含 log 挂载，而现场增加了 `resources: {}` 和 `AUDIT_KEEP` 环境变量：

```yaml
# 更新后保留外部字段；volumeMounts 与 log volume 均已删除
containers:
  - name: broker
    resources: {}
    env:
      - name: AUDIT_KEEP
        value: retained
```

无需为了删除挂载而在 MID 人工保留 `containers: [{name: broker}]`。如果现场把历史挂载路径改成了另一个值，则这次删除被阻断，挂载和卷都保留，返回冲突；处理冲突后可用同一份 MID 重试。

## 记录与故障恢复

- 每个 owner、逻辑来源和目标 UID 对应一个固定名称的私有 Secret。历史使用 ownerReference；目标 annotation 只存 Secret 名和本次操作编号。语义载荷继续使用 v2。普通历史与暂停快照分别存储。
- 一份记录保存上次成功声明，以及至多一笔待确认操作。准备阶段使用 Secret 的 resourceVersion 防并发，保存本次声明、删除检查和有歧义的空列表拓扑。业务请求使用目标 UID/resourceVersion 防并发。正常运行、dry-run、实际响应与恢复使用同一计划的检查规则。
- 真实响应满足计划且回执匹配，才把候选变成成功历史。确认只更新 Secret。响应丢失、字段被重新插入或最终确认失败时，保留待确认操作；重启后按固定名称读取，目标指针被清除也能找到。同一份声明可重试。外部变更与已写入结果无法区分时，返回可诊断错误并保留历史；只有证据足够才处理后续撤回。
- 旧请求仍可能迟到且目标 RV 尚未变化时，控制器先用带 UID/RV 的维护标签更新令旧请求失效，再重新读取。默认化、并发或准入修改业务字段时须先通过对应检查。多个资源分别提交。
- 原有不可变 v1/v2 记录在正常更新时接续。v1 的旧列表沿其原有拓扑判断删除；迁移不会根据新识别出的 name 深入旧 atomic 成员追删。v2 的未确认删除须先核对候选与旧基线。旧不可变记录沿原有 GC 协议回收；新记录在同一目标 UID 存活期间保留。删除 owner 时由 Kubernetes 回收附属 Secret。
- 目标已不存在或同名对象 UID 已变化时，后台可以回收绑定旧 UID 的未引用记录；CRD/API 端点不可用不等于目标对象不存在。当前指向记录、身份不符、损坏记录、权限/网络失败、未知发布协议均不会按年龄强删。缺少发布协议信息的记录保留给正常版本替换或 owner GC。清理失败记录脱敏日志，下一轮重试，不阻断正常 reconcile。删除带 Secret UID/RV 条件，避免并发元数据修改后误删。
- 记录绑定 owner UID、资源 UID/GVK/key、Configuration 名称以及合并模型。已有当前 owner UID 来源标记，或同命名空间有效 controller ownerReference 的资源继续按严格身份校验写入。目标上的 owner UID 标记丢失时，只要能核实稳定历史或旧历史同时属于当前 owner、来源和目标 UID，便继续按这份历史处理撤销。新 owner 接管时另建历史。同名 MCF 重建仍视为相同逻辑来源，正常写入更新 Configuration UID，不因 UID 变化拒绝该来源。
- 历史 Configuration 缺少 owner UID，或标记中的旧 owner 已不存在时，只有 `middleware.cn/app` 与当前 owner 名称相同、`middleware.cn/configurations` 与当前 Configuration 名称相同，且没有冲突归属，才允许接管。其他 controller、仅有 Helm 标记、其他来源仍拒绝写入。旧 controller 引用仅允许接续同命名空间、同名同类型的已删除 OpenSaola owner。
- 接管前通过直接 API 读取检查 MID/MO：旧 owner UID 仍存在、其他 owner 的清单指向同一资源，或其他同名 owner（含跨命名空间、不同 MID/MO 类型）直接或经 baseline 引用该来源，都视为冲突；暂停和删除中的 owner 也计入检查。不同名称实例复用同一模板本身不构成冲突。业务 dry-run 和新记录准备完成后再次检查 owner 状态与冲突归属，再以一次 JSON Patch 同时提交字段、归属标记、controller 引用及新记录指针；资源 UID/RV 变化会触发重新读取和校验。
- 写入器返回实际发布的目标 UID。登记清理清单前再次回读，UID 一致才可登记。期间发生同名替换时，本次发布返回可重试错误。清理继续要求 UID 与归属标记匹配。CRD 始终保留为 `orphan`，不通过删除重建完成接管，已有 CR 不受接管本身影响。
- Secret 缺失/损坏、资源被同名替换、真实合并拓扑变化、API schema 无法获取时，停止推断删除并返回可重试错误，不退回全量 Update。修复记录或 API 可用性后正常重试。固定名称的历史可独立于目标指针找到。记录损坏应恢复受信备份并核对 owner 与目标 UID。
- 一次性 Action 按目标的 Kubernetes schema 定位关联列表成员。例如同一卷分别挂在 `/logs` 和 `/audit`，只设置 `/audit` 的 `readOnly` 时，按 `mountPath` 定位这一个挂载点，再通过带 UID/RV 的 JSON Patch 提交差异。省略项保留；不会以 Action 身份提交整个对象，也不允许编辑历史指针、操作编号或 GC 标记。
- schema 的描述、validation-only 变化及无关 Kind 变化不会使记录失效；真正的字段类型/list-key/atomic 变化会。真实集群从 v2 发现切换到 v3 时，暴露的默认值也可能不同并触发保护，不能承诺跨 Kubernetes 版本永不失效。支持 OpenAPI v3；只有旧集群 `/openapi/v3` 根端点也为 404 时才使用真实 OpenAPI v2；新 CRD 的单个 GV 端点尚未发布时返回可重试错误，403/超时不降级猜测。
- 记录采用 zstd 快速压缩，复用单并发编码器/解码器；压缩确实变小时保存为 `intent.json.zst`，否则保存原始 `intent.json`。业务写入和后台清理使用同一解码逻辑，校验压缩帧 checksum，缺失、损坏或同时存在两种编码时失败关闭。
- 不设置应用层记录大小或解压后 JSON 大小上限，不截断记录。新建前先对业务资源做 server dry-run，计算包含 API 默认值的记录，再对真正编码后的 Secret 做 server dry-run；最终写入由 Kubernetes 校验 Secret、请求体和存储限制。Secret 被拒绝时返回 API 错误，已有业务字段和记录指针不变。MCF 是模板，实例参数、渲染展开、原始/有效期望以及待确认删除内嵌的上一份历史可能使记录更大，因此 MCF 可发布不等于记录一定能写入；压缩可以减少这种差异，但不绕过 Kubernetes 的限制。非确定性 admission 在 dry-run 与实际请求间仍可能返回不同内容，不能把 dry-run 当成跨请求事务。Secret.stringData 会规范化成 data 后记录。

## API 默认值与剩余边界

原生 PodSpec 中，同一 `volume` 可以挂在多个位置。Baseline 与 MID 合并时以 `mountPath` 区分挂载。老 MID 只写 `name` 时，如果基线中恰好有一个对应路径，可继续使用；存在多个候选时会提示在 MID 指定路径。自定义 CR 中缺少结构描述的包参数沿用包的命名列表规则。

原生 PodSpec 的 `initContainers` 按顺序执行。声明调整两个初始化容器的先后顺序时，包渲染和写入都会落实该相对顺序，同时保留未涉及的容器。原生探针有来源证据时，撤销 `periodSeconds: 20` 可恢复 Kubernetes 默认的 `10`，撤销整个探针可一并清理相关默认字段。若外部已改写需要删除的子字段，更新报告冲突。

API Server 会添加模板没有写的字段，例如 Secret volume 的 defaultMode。额外字段未必是外部操作，也不能一概当成自己的字段。

记录同时区分原始渲染期望与本次 API 请求补全后的有效期望。只有本次提交相对服务端结果新增加的字段，位于模板明确提供的关联列表项内部、父结构仍由模板提供，且与 schema 或已识别的内建默认值一致，才记录为 API 补全字段。准入临时注入的普通子字段不进入删除依据。不会把整份 live 或此前已有的默认值/外部字段收入基线。后续同一列表项仍存在时携带已记录默认值；整项移除时一起参与三方比较。因此 JSON Patch 新增的 volume 也可以在下一轮删除，不再依赖旧 SSA 归属。

对首次记录之前已经存在、但模板没有写出的默认字段，无法与外部添加可靠区分，仍保留。若递归删除留下非法结构，例如旧 Secret volume 只剩未记录的 `defaultMode` 而缺少必填 `secretName`，server dry-run 会拒绝整个资源；其他 env、mount 和 volume 均不提前删掉，历史也不推进。不能承诺自动清理所有旧字段。已记录默认值被外部改写时，同样按删除冲突拒绝；新增独立子字段则保留该字段并清理其余可删子字段。

声明校验按 schema 语义处理关联列表顺序、数值表示和 Quantity；对已注册原生类型（含官方 v1/v1beta1 CustomResourceDefinition）允许合法的 `omitempty` 规范化。opaque CR 的零值或空值不会凭猜测视为已生效。声明按投影校验，删除按本次历史差异路径校验；不要求整个资源与模板逐字相等。schema 已知默认值的合法回填和原生类型的合法空值省略可接受，opaque 未知字段不猜测默认值。没有声明变化、业务语义也没变化时，不反复发布记录。

opaque 子树的 name 合并不补造业务 schema。未知互斥字段（例如某 CR 自定义的 value/valueFrom）可能在保守迁移后同时保留，API 若无校验也无法自动识别其非法性；不能承诺所有这类转换都成功。应提供准确的 CRD schema，或通过显式配置和业务验证解决。

同样，首次建立记录之前就已经省略的 Nacos auth 字段不会凭空识别为可删。建立记录以后真实发生的开→关变化才有明确差异依据。PreAction 在内存里把 `{}`/`[]` 合并成什么仍遵循原包约定；如果最终渲染没有省略字段，writer 无法把它当成删除意图。本次不改变全局 MergeMap/MergeArray 语义。

`ResourceIntentPreserved` 日志/Event 说明保留独立外部字段的原因，只输出脱敏路径。历史字段被改写或受外部 Apply 保护导致不能删除时，返回删除冲突错误，不能仅发保留事件后报告成功。诊断分类为 `Conflict`；声明或删除未落实为 `Validation`，不会因字段路径含 `template`、`tls` 被误归类。Kubernetes APIStatus（例如 RBAC 拒绝）仍优先。多资源更新不是跨对象事务，目标字段写入成功也不等于实例已完成滚动。

暂停恢复的 `reconcileOverrides` 仍采用 JSON merge patch，数组按整体比较。后续声明改变同一数组时，该数组的旧 override 停用；声明恢复原基线后，尚存的 override 可能再次生效。本次普通写入器的 name 合并在 override 之后执行，没有改变这一上游生命周期规则。

## 主资源删除后的重建

主 CR 删除事件触发重建，并以每 5 分钟的只读存在性检查兜底。目标存在时只校验身份，不重放业务字段；目标缺失时，在内存中重放纯 CUE 渲染及有效 override，用 create-only 请求重建，再建立历史记录并恢复监听。并发创建同名对象时重新读取和校验，不覆盖该对象的业务字段。

暂停中、删除中的 MID 跳过重建。读取失败、归属冲突、缺少渲染依赖和非 CUE 的 CMD/HTTP PreAction 返回 `PrimaryResourceCheckFailed`，重试时先检查主资源。若已存在且归属正确，恢复可用状态。创建、历史记录或监听恢复失败返回 `PrimaryResourceRebuildFailed`，下次重试继续处理该主资源。外部操作和其他 Configuration 均不参与重建。Job 等被现有写入器认定为一次性不可变资源的对象不走此自动重建路径；batch/v1 CronJob 属于可变资源。修复后同 generation 可以重试。该流程恢复主 CR，不承诺恢复已经删除的数据或存储。

## 回退

回退控制器不能恢复已经删除的字段，也不能通过直接回放整份 Secret 中的模板覆盖现场。需要恢复具体业务字段时，重新在 MID/包声明中配置它们并验证结果。v1.4.0 无法读取本次生成的 v2 普通记录，会拒绝相应更新；不能将直接回退二进制视为无影响操作。旧控制器不能处理新的可变历史 Secret。回退应先按实例备份和验证兼容性，保留同一时间只有一个活动版本的控制器。

本机测试见 `internal/k8s/intent_local_test.go`（integration tag，限定专用 loopback kind context）；纯算法、schema/存储安全和真实 controller 流程分别有单元及 envtest 覆盖。测试结果与已知保留项必须分开报告。
