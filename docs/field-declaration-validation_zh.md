# 声明字段写入优化与验证记录

## 测试充分性审查后的修复

| 审查反例 | 本轮新增的验收 |
| --- | --- |
| Configuration 的 owner 注解遗失，撤销 ClusterRole 规则却返回成功 | 验证 owner、逻辑来源、目标 UID 相符的当前历史和旧不可变历史都能继续删除。若外部修改过该规则则返回冲突。 |
| 写入 ConfigMap A 后，同名 B 在登记前替换了 A | B 即使复制生命周期标记也不能进入本次发布清单。 |
| 首次 POST 已提交但响应超时 | 核验本次操作回执后只为明确声明建立历史。后续重启、撤回可以清理旧声明，保留只在回读中出现的外部字段。回执缺失时本次返回错误。 |
| 原生探针撤销或初始化顺序变更 | 四种探针 handler 覆盖撤销单个设置、整探针及外部改写。原生 Deployment 回读声明的初始化顺序，并保留外部步骤。 |
| 一个卷挂在 `/logs` 和 `/audit` | 分别验证 Baseline 与 MID 渲染以及一次性 Action。只改后者的 `readOnly` 时，前者的路径和权限都保留。 |
| MID 向主 CR、Configuration、PreAction 或后续 Action 传递大整数 | 精确断言区分 `9007199254740992` 和 `9007199254740993`；覆盖 int64 极值、配置模板独立取值、暂停采纳的双向变化和旧快照读取。 |
| 路径 fuzz 只变化键名 | 新增结构变化的 fuzz，组合嵌套对象、命名列表、标量列表、null 和空列表，逐次核对落实、重复执行、撤销和重启验收，保留外部兄弟字段。 |

普通单测和 race 均纳入 Controller，用 envtest 运行全部 internal 包，包含 Action。Redis 和 Kafka 实际工作负载的运行条件见[工作负载回归入口](field-workload-tests_zh.md)。本轮普通单测和 race 分别通过 27 个测试包、1,052 个条目（含子测试）；Kubernetes 1.35 envtest 命令通过 23 个 internal 包、1,054 个条目，Controller 的 Ginkgo 套件 31/31 通过。三组条目有重叠。字段结构 fuzz 运行 62,890 次，路径 fuzz 运行 54,956 次。默认和带 envtest/integration 标签的 lint 均为 0 项，manager 构建及 Makefile、发布脚本回归通过。

### 本机工作负载实测

| 运行 | 实际检查 |
| --- | --- |
| Redis 7.2.7，独立 Kubernetes 1.34.3 集群 | 完整 `TestLocalIntentReconcile` 通过，含父级共 44 项、93.642 秒。新建与接管两种入口均在滚动后通过 redis-cli 从 PVC 读回原值。 |
| 从正式版 v1.4.0 升级，Kafka 3.9.1 | 旧版从空集群装原包，首个 Ready broker 写入 SASL 消息。换用本轮候选后，先保留挂载触发一次正常 MID 更新、确认历史迁移，再撤销挂载。第二个 Ready broker 读回升级前消息。向运行中 MID 再加挂载，第三个 Ready broker 实测文件读写通过。MID 及 Kafka CR 的 UID 始终相同。 |
| 最终 dev 候选独立首装，Kafka 3.9.1 | 使用原包从空集群安装。在同一个 MID 上，带挂载运行、撤销、重新添加均得到 Ready broker。核对内核挂载表，实际写读 `/root/logs`，并在撤销后通过 SASL 消费滚动前的消息。三个 Pod UID 不同，MID 和 Kafka CR 的 UID 一直相同。 |

最终独立首装的 manager SHA-256 为 `e9b045d690f7b593d9afa50a7f280b8f040132ef8c0422f2bf8fa7872139d24a`。两轮 Kafka 与 Redis 的临时 Kind 集群均已删除。

升级测试使用的候选二进制摘要为 `c1e13e247eee2549c3dc1f25bfa92f9656d06c8affeba71b73e0f4bb2a0cfe65`。旧 atomic 历史首次更新时已撤销的细分字段，仍按旧记录已有的粒度判断。

## dev 统一字段写入实现

共享资源使用一套字段计划。渲染负责合成业务声明。对已有对象，写入器在一份计划中决定要落实的字段、可以删除的字段以及本次验收项，dry-run 和真实响应均使用同一套检查。OpenSaola 在 owner 命名空间保存固定名称的私有 Secret，记录上次成功声明及至多一笔待确认操作。旧 v1 合并器只在对比测试中运行。

| 历史问题 | 固定的验收行为 |
| --- | --- |
| 未变化的 MID 声明曾被现场漂移掩盖 | 下次正常更新恢复声明，同时保留未涉及的兄弟字段 |
| 旧记录是命名列表，现场已变为对象或标量 | 撤销声明时保留现场并报告冲突，整个目标本轮业务变更均不提交 |
| 应删除的成员在写前已缺失，写后又被插回 | 关联键、复合键、set、opaque 命名列表均核对实际响应 |
| 目标历史指针被清除，旧字段又被插回 | 用固定名称找回待确认记录，继续按原计划检查 |
| 外部 SSA 管理同一容器的 env | 保留 env；可撤销本方 mount，共同管理待删叶子时报告冲突 |
| 移除 replicas 或 Secret 卷的 mode | 有证据的内建默认值可通过验收；同配置重复执行不触发额外业务更新 |
| 删除已执行且默认值已回填，随后确认超时 | 重试识别已提交操作，继续确认 |
| 同一操作删除 A、新增 B，确认前又撤回 | 有回执且字段符合时按已落地的候选清理 B；证据不足时保留历史并报错 |
| 字段名包含点，另有嵌套同名路径 | 结构化路径区分两者；旧 v1 迁移和新计划均有覆盖，复合键保留整数精度 |
| 重建收到 GET 结果，含外部改写的字段 | 仅真实 POST 响应能提供首次默认化证据；不把外部值收为将来删除权 |
| 健康实例短暂读取失败，且配置非 CUE PreAction | 同 generation 再检查；若主资源仍在，恢复健康，不执行副作用 |
| 暂停后调整或撤销原生容器 | 按暂停前后实际状态决定采纳，并识别 Kubernetes 真实添加的容器默认值 |
| Action 只改一个字段 | 增量请求带 UID/RV，保留其他字段的归属；同时禁止通过 Action 编辑私有历史 Secret |

[计划及边界回归](../internal/k8s/intent_declared_regression_test.go) · [真实 API 故障注入](../internal/k8s/resource_intent_policy_envtest_test.go) · [Configuration 接管与原生挂载](../internal/service/middlewareconfiguration/adoption_envtest_test.go) · [Action 字段归属](../internal/service/middlewareaction/action_edit_envtest_test.go)

### 上一轮统一写入实现的自动化结果

| 运行项 | 实测结果 |
| --- | --- |
| `go test ./... -count=1` | 27 个含测试的包通过，Go 报告 1,027 个通过的测试条目（含子测试） |
| 全部带 envtest 标记的包，Kubernetes 1.35.0 API Server 与 etcd | 4 个包通过，Go 报告 694 个通过条目，其中包括普通单测。Controller 的 Ginkgo 套件内 30 个规格通过 |
| 8 个相关包的 `go test -race` | 8 个包通过，Go 报告 771 个通过条目；覆盖写入、暂停、重建、Action、Configuration、watcher、状态和包合并 |
| 字段路径 fuzz | 15 秒执行 1,607 次，覆盖含点的键、嵌套路径和持久化往返 |
| 构建、静态检查、发布脚本 | manager 构建成功，默认与 envtest/integration 标签下的 golangci-lint 均为 0 项，Makefile 及发布自动化测试通过 |

这些运行方式有重叠的单测，计数按各次运行独立记录。仓库原有的 `Test_executeCmd` 是显式跳过的外部命令占位用例；禁止重放 CMD/HTTP PreAction 的行为由真实 controller 和故障恢复用例覆盖。

### 上一轮统一写入实现的本机工作负载

最终控制器二进制 SHA-256 为 `98747dd8a0681f34994e2b1e6339a2fb37cf2fc63c4e89bf7598bcc03ed47255`。原生 Deployment 在撤销声明后从 3/3 回到 1/1，目标 UID 相同，volume 和 mount 已移除。独立 SSA 管理者 external-audit 的 env 在 managedFields 和新 Ready Pod 中均得到确认。

本机 Kubernetes 1.34.3 的专用 integration 集群完整通过 42 个独立场景（44 个 Go 测试条目含父级，124.573 秒）。Redis 7.2.7 以新建、接管已有 Deployment 两种入口滚动。两次均落实 KEEP=after，保留外部 EXTERNAL，PVC 均读回写入的测试值。首轮控制面重启出现过一次滚动超时，整套复跑通过后已清理临时集群。

原包 Kafka 3.9.1 的实测配置和结果如下。旧 dev 控制器先建立已确认的不可变 v2 记录，再由最终二进制接续。切换时 Kafka Pod 尚在初始化，此部分验证的是存量历史接续。另用最终二进制从原包新建 CRD、Operator 和 MID 验证首装。

| 场景 | MID、现场与实测结果 |
| --- | --- |
| 普通更新修复已有声明的漂移 | MID 保持 `volumeMounts[name=log].mountPath=/root/logs`。现场改为 `/root/external-drift`，普通更新后为 `/root/logs`。Ready broker 的内核挂载表确认生效，外部 env 保留 |
| 撤销挂载 | MID 省略整个相关容器声明及 volumes 后，Kafka CR 和 StatefulSet 均移除挂载，broker 的外部 env、resources 保留 |
| 发生删除冲突 | 注入外部改写后，本轮整个 CR.spec 和已确认历史保持原值，观察代际不推进。处理冲突后按同一 MID generation 重试成功，Ready Pod 已卸载 |
| 首装、读写与卸载 | 新 MID 声明 `volumes: [{name: log, emptyDir: {}}]`，broker 声明 `volumeMounts: [{name: log, mountPath: /root/logs}]`。在 Ready broker 实际创建、写入、读回并删除临时文件。撤销后另一个 Ready broker 的 `/proc/mounts` 已无该路径 |
| 业务协议与清理 | 使用 SASL/SCRAM 创建临时 topic、生产并消费消息，卸载后再次验证。临时 topic、客户端文件及两个专用 Kind 集群均已清理。既有 formal 22 个和 smoke 12 个 Pod 的 UID 与测试前一致 |

## 早期候选的历史测试记录

以下保留在稳定历史 Secret 引入之前的两轮结果，便于区分当时的机制和今天的行为。当前操作规则见[声明字段写入与历史字段删除](resource-intent_zh.md)。

日期：2026-11-18。修改位于 `dev`，以 v1.4.0 的字段写入行为为旧版本基线。按合并算法、生命周期/故障恢复、本机 Kafka 验收三个角色分工，交叉检查写入与记录回收。未发布版本、未提交或推送；任务开始前的 11 个已修改文件保持原样。

## 改了什么

1. **当前最终声明负责写入，历史负责安全删除。** Baseline、MID、PreAction 和有效暂停恢复 override 合成后的字段，只要仍明确声明，每次正常更新就落实；不再因为“与上次声明相同”而保留错误实际值。
2. **按真正的字段粒度合并。** schema 有键/set/atomic 定义时遵循定义；只有 opaque 子树中的唯一 name 对象列表递归按 name 合并。保留声明未涉及的独立字段。显式 null 和 atomic 整体值仍具有覆盖含义。
3. **检查实际写入结果。** 合并结果、server dry-run 和实际提交响应均检查声明投影及计划删除路径；没有落实则返回错误。同配置可重试，MID 不因失败推进已处理代际。记录指针补偿不回滚业务字段。
4. **正常更新自动接续历史。** v1 记录先按旧规则判断删除，再落实新声明，成功后转为 v2；无需客户运行迁移 CLI。失败补偿采用旧历史的新 Secret 副本，避免旧记录被 GC 后留下悬空引用。
5. **补主 CR 删除重建。** 删除事件和 5 分钟只读存在性检查触发独立重建；仅安全渲染主 CR，使用 create-only 请求，不重放 Configuration 或 CMD/HTTP PreAction。暂停恢复继续比较两份实际状态。

6. **整体省略成员时递归清理。** 历史 broker 只声明挂载、现场增加 resources/env 时，删除历史挂载并保留这些外部子字段及 name。不能删除的历史叶子或外部 Apply 冲突会阻止整个目标资源写入，避免卷已删除、挂载还在。
7. **删除依据持久化至确认完成。** 有删除计划的候选 Secret 内嵌上一成功历史，真实响应检查后在 Secret metadata 确认。超时、补偿冲突、确认失败和重启均保留删除依据；暂时已缺失的字段也必须检查没有被重新注入。
8. **原包首装规范化兼容。** 官方 CRD 的 `preserveUnknownFields: false` 被 API 省略是合法原生规范化，不能误报字段未生效；普通未知 CR 字段仍严格校验。

规则详见 [声明字段写入与历史字段删除](resource-intent_zh.md)。没有改全局 MergeMap/MergeArray 的空列表契约，没有修改 Kafka 适配包或第三方 CRD。

## 自动化覆盖范围

| 范围 | 验证的行为 |
| --- | --- |
| 字段合并 | 未变声明纠正漂移；缺失/null 容器补挂载；嵌套 name 合并；整成员省略后递归删除历史子字段；外部容器/env/resources 保留；外部改写删除字段则整资源拒绝 |
| schema 边界 | 自定义键、复合键和默认键；set；显式 atomic 和默认 atomic；无键列表；模糊/重名列表拒绝；外部 Apply 对删除的保护 |
| 规范化 | Quantity 数值等价、float/int 等价、原生 false/空值省略；未知 CR 零值不猜测；未来原生字段不被忽略；输入不被修改 |
| 无变化更新 | 相同声明和实际值不写 API、不发布新记录；仅新增/删除声明责任时，即使实际值不变也更新记录 |
| 版本与存储 | v1→v2、无历史初始化、v1/v2 读取、未知版本保留、暂停快照不可混用；已有压缩、大小、身份、schema hash 和 GC 测试继续执行 |
| 真实 API 故障注入 | dry-run/实际响应改写和删除重注入；准备记录后暂停；资源版本冲突；同配置重试；确认失败/响应丢失/重启；旧记录被 GC；补偿冲突与 fence；L 已缺失但 admission 再插入 |
| 生命周期 | 真实 informer 删除事件→队列→Controller→重建；纯 CUE 与有效 override；非 CUE 失败；同 generation 重试；暂停/删除/归属保护；不重放 Configuration |
| 暂停恢复 | 实际前后差异采纳；失败保留恢复状态；merge 结果经真实 writer 写入；后续无关 MID 更新仍保留有效采纳结果 |
| 接管与清理 | MID/MO、历史 Configuration 接管、同名来源重建、跨命名空间和整资源删除策略的现有集成测试 |

真实 API 测试使用 envtest 的 Kubernetes 1.35.0 API Server。故障通过包裹真实客户端注入，涉及的资源更新、资源版本和 GC 删除实际到达 API；这不等同于部署了一套生产 admission webhook。

可复验命令：

```sh
go test ./... -count=1 -timeout 6m
go test -race ./internal/k8s ./internal/controller ./internal/service/middleware ./internal/service/middlewareconfiguration ./internal/service/watcher ./pkg/tools -count=1 -timeout 6m
KUBEBUILDER_ASSETS="$PWD/bin/k8s/1.35.0-darwin-arm64" go test -tags envtest ./internal/controller/... ./internal/k8s/... ./internal/service/middlewareconfiguration/... -count=1 -timeout 6m -v
bin/golangci-lint-v2.11.4 run
make test-makefile test-release-automation
go build -o /tmp/opensaola-manager-check ./cmd
```

测试入口：[合并算法](../internal/k8s/intent_declared_merge_test.go)、[原生字段](../internal/k8s/intent_declared_native_test.go)、[写入与故障恢复](../internal/k8s/resource_intent_policy_envtest_test.go)、[删除事件重建](../internal/controller/middleware_rebuild_envtest_test.go)。envtest 在任何测试开始前设置独立 kubeconfig；普通 Controller 单测也隔离默认集群连接。

## 第一轮本机 Kafka 的实际对比

在独立 Kind 集群中，先运行正式 v1.4.0 复现，再在**同一个失败实例**上切换 dev；保存原始 v1 Secret、MID、CR、StatefulSet、Pod 和 API 审计。原始记录没有 Effective 键，报告不把缺省值当成实际 null。此次证据补齐了早期排查中缺少原始历史记录的限制。

实际环境为 Kubernetes v1.34.3、Kafka 3.9.1、Kafka Operator v1.16.1；使用基于 Kafka 1.16.2-1.0.0 的原始单实例定制包，一副本、1Gi PVC。Kafka-init 镜像只有 amd64，本机沿用仿真；不宣称完成全链路原生 arm64 验证，也不以单副本 Kafka 代替所有中间件/HA 场景验收。

```yaml
# MID 明确声明；容器名按实际 Kafka 包填写
volumes:
  - name: log
    emptyDir: {}
containers:
  - name: broker
    volumeMounts:
      - name: log
        mountPath: /root/logs
```

| 检查 | v1.4.0 复现 | dev 验收 |
| --- | --- | --- |
| 挂载写入 | volumes 有 log，containers 为 null | CR、StatefulSet、Pod 均有 log 挂载，容器 mountinfo 可见 /root/logs |
| 多地址开关 | MID EnableExternal=true，实际 init.env 仍 false，Broker 启动失败 | 实际 init.env=true，Broker、Exporter、Manager Ready |
| 声明未变化但实际漂移 | 已有历史会阻止再次落实 | 将实际 mountPath 改为 /root/external-drift，只改无关 MID 参数，恢复 /root/logs |
| 修改挂载路径 | — | MID 改为 /root/logs-updated，实际 Pod mountinfo 验证通过 |
| 子字段删除 | — | 保留 broker 的 name、移除 volumeMounts 与 log volume 后，新 Pod Ready，两个日志挂载路径均不存在 |
| 外部独立字段 | — | AUDIT_KEEP 环境变量与 CR 根 annotation 保留，未收进声明历史 |
| 协议 | — | 内部 Service、两个 NodePort 分别完成创建 topic、生产、消费一致性检查并删除 topic |

旧失败 Pod 的 OrderedReady 更新曾被卡住。修正 CR/StatefulSet 后，通过包提供的停机动作和 MID 恢复副本完成一次实例停启，PVC UID 不变；没有直接 patch/delete StatefulSet 或 Pod。该操作属于旧失败实例的运行恢复，不能写成控制器自动滚动成功。

NodePort 请求从 Broker Pod 发往 Kind 节点 IP，验证节点端口协议，不代表主机或公网可达。该场景的外网声明与包动作输出一致，但 NodePort Service 在测试集群单独创建，未将此项当成 exposure Action CMD runner 的完整验收。

## 删除与其他边界

第一轮实测曾发现：整个 broker 容器声明被省略后，现场额外的 `resources: {}` 导致整个容器和挂载被保留，但独立 log volume 已删除。该行为是本轮继续修复的回归，不能作为合理限制或要求用户自行绕过。

本轮改为递归删除历史子字段：历史挂载未被改写时，删除挂载和 log volume，留下 broker 的 name、resources 和外部 env；历史挂载被外部改写时，整个目标资源停止写入、历史不推进。解除冲突后原 MID 同一 generation 可重试，无须人为增减配置。

首次记录之前已有的默认值仍无法自动认领。真实 API 测试证明：旧 Secret volume 中只有未记录的 `defaultMode` 留下而缺少必填 `secretName` 时，dry-run 拒绝整个资源，env/mount/volume 与历史指针均不变。新建并正确记录 API 默认值的资源可正常整项删除。opaque CR 的未知业务引用关系仍依赖 schema 或中间件功能验证。

另外保留以下明确边界：

- Kafka Baseline 中 `metadata:` 是显式 null，覆盖该节点内的额外 annotation 符合声明规则；独立字段保护不是无条件保护任何层级的字段。
- v1 首次迁移不追删旧 atomic 内部无法确认归属的字段；opaque 互斥字段仍依赖准确 schema 或业务验证。
- 本次没有实现现存主 CR 的持续字段漂移修复；普通更新、初次发布和恢复时落实声明。5 分钟检查仅补缺失主资源。
- 暂停 override 仍按 JSON merge patch 处理数组：同一数组的声明变化会停用整个旧数组 override；恢复原数组后，未删除的旧 override 可以重新生效。新增回归确认该既有行为，未将其改为逐 name 管理。
- 多资源写入没有跨对象事务。API 字段写入成功不等于中间件已经完成滚动或业务已就绪。
- Job 等一次性不可变资源不自动重建，含非 CUE PreAction 的缺失主 CR 也不会通过执行外部副作用来恢复。
- v1.4.0 不能读取 v2 普通历史记录，直接回退二进制不是透明操作。

## 第一轮自动化结果（历史记录）

| 检查 | 结果 |
| --- | --- |
| 全仓 `go test ./...` | 通过 |
| k8s、Controller、middleware、Configuration、watcher、tools 的 race 检查 | 通过；新增算法/no-op 及最终隔离后的 Controller 另行通过 race |
| 完整 envtest | 通过：Controller 29 个 Ginkgo 场景及普通单测、k8s 全组、Configuration 全组 |
| 新 writer 真实 API 场景 | 11 个通过；故障恢复场景另行通过 race |
| golangci-lint | 0 issues |
| Makefile 与发布脚本契约 | 通过 |
| 完整 `./cmd` 构建、diff 空白检查 | 通过 |

完整集成测试曾发现测试配置单例提前绑定默认集群，已将隔离移至 TestMain，并增加实际动态客户端读取本次 namespace UID 的断言；原失败随机种子和最终全组均通过。没有靠延长超时跳过该问题。

性能基准位于 [intent_declared_merge_benchmark_test.go](../internal/k8s/intent_declared_merge_benchmark_test.go)。Apple M4 / darwin arm64，500ms × 3 的单次纯合并结果：100 个成员约 1.5–1.7ms、1.97MB 分配；1000 个成员约 13.8–17.3ms、20.60MB 分配。仅该输入形状接近线性增长，未包含多轮投影校验、API 和记录编解码；临时分配仍可后续优化，不能把该数值当作完整更新耗时。

第一轮二进制 SHA256：`200591c73b92375039033a5cb391bab82b1f1426978c90ab8a69bc48079ca0d5`。切换同一实例再次验证新增 /root/logs、保留容器 name 删除挂载子字段及卷，真实 Pod mountinfo/路径/Ready 检查均通过，外部环境变量和根 annotation 保留。

临时 topic 和客户端文件无残留；本轮主机控制器正常退出，Kind `kafka-field-fix-1118` 已删除。默认 context 仍为 `kind-opensaola-formal`，原 formal 的 22 个 Pod、smoke 的 12 个 Pod 数量和 UID 全部不变。原始证据保存在本机任务临时目录的 `kafka-runtime-uih8uyum/`，包括独立报告、`dev-final-summary.json`、`topic-cleanup.json` 和 `cleanup.json`；含测试凭据的原始档案不进入仓库。


## 第二轮：整项删除、冲突与持久重试（2026-11-18）

代码由合并算法、生命周期与真实 API、Kafka 运行验收三个角色分工，并独立复审 writer、历史确认、指针补偿和 GC。生产修改限定在 OpenSaola dev，未修改 Kafka 包、CLI 或第三方 CRD。

Kafka 功能验收构建 SHA256：`2004d8fe12821df448915df5e6794eb60b87189ab16de8ff89bbedc64c958f99`。

| 检查 | 本轮结果 |
| --- | --- |
| 全仓单元测试 | 最终代码 `go test ./...` 通过 |
| 相关模块 race | k8s、Controller、middleware、Configuration、watcher、tools 通过；最终 k8s 全包和 writer 真实 API 矩阵另通过 race |
| 完整 envtest | Controller 30 个 Ginkgo 场景及普通测试、k8s 全组、Configuration 全组通过 |
| writer 真实 API | 共 45 场景通过（原 11 + 本轮 34） |
| 静态检查与构建 | 全仓 golangci-lint：0 issues；完整 `./cmd` 构建通过 |
| Makefile / 发布脚本契约 | 通过；未发布制品 |

本轮新增覆盖包括：typed/opaque 整项递归清理、外部字段保留、历史叶子被改写、外部 Apply 冲突、schema 默认回填、无历史初始化、v1 atomic 保守迁移、原生合法删除与必填字段拒绝、dry-run/commit 重新注入、提交响应丢失、补偿冲突、旧历史被 GC、Secret 确认失败/超时、新 writer 从持久记录恢复、撤回待完成删除，以及“规划时字段已无，下一次 API 又插回”的连续失败。

同名成员再次出现的判定也有 8 个真实 API 场景：typed/opaque × dry-run/commit × 只含新外部字段/夹带旧挂载。只补 `resources: {}` 可保留，不能被纳入新原始声明；重新夹带旧挂载则失败，故障解除后原声明可重试。API 故障由真实客户端包装注入，不等同于部署生产 webhook。

测试入口：[删除计划](../internal/k8s/intent_deletion_plan_test.go)、[持久确认](../internal/k8s/resource_intent_confirmation_test.go)、[真实 API 删除矩阵](../internal/k8s/resource_intent_deletion_envtest_test.go)、[Controller 失败与同代际恢复](../internal/controller/middleware_deletion_envtest_test.go)。旧 opt-in 本地 integration 用例的预期已同步为整资源拒绝；本轮仅编译并确认未提供专用 kubeconfig 时会跳过，实际行为由上述 envtest 和独立 Kafka 验收覆盖，没有冒充执行整套旧 Redis integration。

最终基准（Apple M4 arm64，500ms × 3）如下。数据形状为每成员包含 image、历史 env 和外部子字段，批量省略原成员；转换器与 fixture 构造在计时外。

| 操作 | 100 成员 | 1000 成员 | 每次分配（100 → 1000） |
| --- | --- | --- | --- |
| 合并更新 | 1.31–1.33ms | 12.91–13.32ms | 1.99 → 20.75MB |
| 删除计划 | 1.16–1.29ms | 11.63–11.72ms | 1.70 → 17.43MB |
| 删除响应校验 | 0.49–0.52ms | 5.03–5.16ms | 0.497 → 5.23MB |
| 已缺失字段重试计划 | 0.84–0.85ms | 8.80–10.43ms | 1.276 → 13.25MB |

该固定形状扩大 10 倍时，耗时约扩大 9–12 倍，未见明显平方增长；不包括 API、Secret 编解码/存储或完整 reconcile，不能据此承诺任意形状线性性能。


### 第二轮 Kafka 实测结果

仍使用基于 Kafka 1.16.2-1.0.0 的原始单实例定制包，在专用本地 Kind `kafka-delete-fix-1118` 中测试。先由第一轮构建产生真实 v2 历史：broker 只声明 log 挂载；现场另有外部 env、Operator 写入的 `resources: {}` 和 CR 根 annotation。随后切换上述功能验收构建。

| 实际操作 | 验证结果 |
| --- | --- |
| MID 同时省略整个 containers 和 volumes | CR、StatefulSet、最终 Ready Pod 中 log 卷和挂载全部消失；容器内 `/root/logs` 不存在 |
| 检查外部字段 | 同一 broker 的 `AUDIT_KEEP`、`resources: {}`、CR 根 annotation 保留，未纳入新原始历史 |
| 重新声明挂载，现场改成 `/root/external-drift`，MID 再整体省略 | 报 ApplyClusterFailed；整个 CR spec、历史指针不变；卷仍在；generation=5 时 observedGeneration 保持 4 |
| 仅恢复现场路径，不修改 MID | 同一 MID UID、同一 generation=5 自动重试成功；observedGeneration=5，新历史发布；CR/STS/Ready Pod 中卷挂载全部删除 |
| 在变更后的新 Pod 上验证内部 Service | 新临时 topic 生产、消费精确一致；topic 与客户端配置确认删除 |
| 用验收构建重新执行原包首装 | 保存实例证据后删除仅本轮 MID/MO 和两种原始 CRD，再经 CLI 创建 MO；两个 CRD 新 UID、Established，MO Available，Operator Deployment Ready |

省略整个成员后的真实 CR 片段为：

```yaml
containers:
  - name: broker
    resources: {}
    env:
      - name: AUDIT_KEEP
        value: external-preserved
# volumeMounts 和 log volume 均不存在
```

两次删除均为正常滚动，没有暂停/恢复 MID，也没有手动 patch/delete StatefulSet 或 Pod。注入冲突时仅短暂停止本轮主机控制器，保存一致的冲突输入，再重启验证。

首次安装发现的 `preserveUnknownFields: false` 问题已用原包复验：没有改模板，也没有绕过字段校验；官方 CRD 的合法省略被接受。运行环境仍为单副本 Kafka；Kafka-init 的 amd64 镜像使用本机 arm64 仿真，不将内部 Service 成功扩展为 HA、外网或全架构验收。

本轮原始证据位于任务临时目录 `opensaola-delete-fix-20261118-mtjrdam6/kafka-runtime-mpzthkxu/`，包括 `whole-delete-B-D-L-proof.json`、`conflict-rejection-summary.json`、`conflict-retry-summary.json`、`final-reinstall/summary.json` 和独立报告。可能含测试凭据的 MID/Secret/audit 原件不进入仓库。


### 实测后补充的诊断分类修复

Kafka 的真实冲突输出暴露了旧诊断规则的问题：虽然 `ApplyClusterFailed` 和完整冲突原因正确，路径包含 `template` 时却被归为 `TemplateRender`。现已在模糊关键词前识别字段写入错误：历史删除冲突归 `Conflict`，声明/删除未落实归 `Validation`；APIStatus、RBAC 拒绝仍优先。新增 9 个分类测试、status race 与真实 Controller envtest 通过，Controller 明确断言 `causeCategory=Conflict`。

包含该诊断补丁的最终构建 SHA256：`ee617f1cb6a4f2d14794c90f210a80331a36fc98c126ddc01fbf27ecdd21c79a`。与 Kafka 实测构建相比，唯一生产源码差异为 `internal/service/status/diagnostic.go`；合并算法、writer、历史确认、GC 和 CRD 规范化完全相同，已按文件摘要核对。诊断补丁通过 Controller envtest 单独复验；没有声称在已清理的 Kafka 上重新跑过该二进制。


本轮收尾已完成：测试进程正常退出，Kind `kafka-delete-fix-1118` 已删除，临时 topic、客户端配置及本轮镜像缓存已清理；默认 context 仍为 `kind-opensaola-formal`。formal 22 个和 smoke 12 个 Pod 的数量、UID 全部与测试前一致。代码仍在 dev 工作区，未提交、推送或发布；任务开始前 11 个已修改文件的 diff 与原快照逐文件一致。

诊断补丁合入工作区后再次执行全仓单测、全仓 lint 和完整构建，全部通过。自动化日志、构建文件摘要及前后源码对比保存在本机任务目录的 `verification/`，未将测试凭据加入仓库。
