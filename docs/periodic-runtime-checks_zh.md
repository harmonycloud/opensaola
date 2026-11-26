# 中间件专项复测

运行时包、权限、调度或存储逻辑发生相关改动时，以及定期兼容性回归时，执行下列场景。原包矩阵负责日常首发、协议读写和清理。专项记录与当轮矩阵使用相同的候选提交、包版本及镜像摘要，单独列出每项结果。

## 测试准备

从本地私有 fixture 取得真实包、必要的 MO/MID、依赖清单和匹配的客户端。安装候选控制器及其 CRD，使用 `saola install` 和 `saola create -f` 正式发布。测试账户从本轮的 Secret 注入，归档配置与客户端输出时清除凭据。需要多个调度节点时，准备 Kind 配置文件，在 `nodes` 中分别列出一个 control-plane 和足够的 worker，为工作节点设置本轮的 zone 标签。

```bash
# 多节点专项使用独占的本机临时集群。FIXTURE_RUNNER 是本地包含下列步骤的脚本。
KIND_TEST_CONFIG="$MULTINODE_CONFIG" bash hack/with-test-kind.sh bash "$FIXTURE_RUNNER"
```

在脚本内通过 `kubectl config current-context` 核对本轮集群。在业务开始前记录 MID、主 CR、Pod、PVC 的名称和 UID；记录采样开始、截止时间、端点、响应及摘要。环境缺少镜像、相应版本的原包、凭据或合法存储类，记作阻塞。针对实际投产的包和拓扑填入 `NS`、`MID`、`STS`、`POD` 等变量。原包输入结构和基础发布路径见[原包回归矩阵](middleware-matrix.md)。

## ZooKeeper：选主和重启 Action

1. 用三成员原包部署到三个不同节点。通过各成员的 `srvr` 或包提供的原生状态命令记录一主两从，再通过 Service 创建本轮唯一的持久 znode，并从三成员独立读取相同值。
2. 记下当前 leader 的 Pod UID 与节点。在临时集群执行 `kubectl cordon "$LEADER_NODE"`、`kubectl -n "$NS" delete pod "$LEADER_POD"`。确认原 UID 已消失、替代成员尚未就绪。剩余成员应选出一主一从，通过原 Service 写入第二个值，两成员要能读回。执行 `kubectl uncordon "$LEADER_NODE"`，等第三个成员就绪并读到第二个值，核对三张 PVC 的 UID。无法选主、丢写或恢复后数据不一致，记录失败。
3. 在独立的健康阶段记录各 Pod UID、容器启动时间和 JVM 进程启动标识。执行 `saola run "$RESTART_ACTION" --middleware "$MID" -n "$NS" --wait 3m`。记录 Action 状态和实际主 CR 维护注解，接着在事先约定的观察窗口内定时采样三成员进程标识；例如每 15 秒一次、持续 6 分钟。每个目标成员都应观察到实际重启，完成后集群重新形成 quorum，原 znode 可读。Action 成功、注解已写入但进程始终未更换时，将“动作已提交”和“重启未兑现”分别记录。前者核对 OpenSaola 执行回执，后者查包的维护注解契约和业务 Operator。

## MinIO：降员读写和双存储类

1. 发布分布式四成员基线，核对四个 Ready Pod 位于不同节点，PVC 均 Bound。用签名 S3 客户端经 Service 写入对象，保存本地 SHA-256。使用各成员独立的访问端点读回并核对字节。
2. 在只运行该实例的测试命名空间，先记录 Pod 数量，设置仅限本轮的 Pod 配额，使删除一个成员后无法补足第四个 Pod。确认替代成员因配额暂时无法创建。经 Service 写入第二个对象，轮询其余三个成员，逐一核对新旧对象的字节。移除本轮配额，待第四个成员 Ready，再对 Service 及四个成员重复采样，记录首次可读时间、404 和摘要差异。持续采样覆盖退出和回归过程；缺成员期间无法满足预期读写、重入后对象缺失或内容不同均记录失败。
3. 对双可用区基线准备两个 zone，按包预期放足可调度节点。先用一个有效 StorageClass 做对照，再用另一个 MID 声明两个不同且有效的 StorageClass。确认两种存储类均能独立动态供给。通过 `kubectl -n "$NS" get sts "$STS" -o yaml` 和 `kubectl -n "$NS" get pvc -o wide` 对照原始声明、各张 PVC 的类、绑定结果和 Pod 实际分布，最后执行签名写、读、删。两个类被渲染成一个含逗号的名字并被 API 拒绝时，记录 API 字段路径和原包模板的实际输出，归入包的存储建模。缺少拓扑标签、供给器或磁盘时保留调度/供给事件，另记环境阻塞。单机 Kind 的多 zone 结果仅覆盖调度域。

## Kafka：严格 ZooKeeper ACL

1. 使用与原包匹配的外置 ZooKeeper、SASL 账户和启用了 SCRAM 的单 broker fixture。给测试路径仅配置指定 SASL 身份需要的权限。同一客户端凭证应能在该路径创建、读取和删除测试 znode，匿名访问应被拒绝。
2. 使用此依赖文件作为原包回归输入，运行 `make test-fields-kafka`。核对 MID 状态、业务 CR 的 conditions、Operator 日志、broker StatefulSet、就绪 Pod，并通过 SCRAM 生产和消费唯一消息。MID 已 Available、实际无 broker 或无法收发消息时，业务验收失败。该入口会在退出时清理自己的 Kind 集群，执行下一项同实例对照时，应在同一个独占的专项脚本内发布并保留实例。
3. 对照时固定账户、路径、包、MID 和主 CR 的 UID，仅在本轮测试路径给同账户追加 digest 权限。记录 broker 创建、Ready 和消息收发是否改变，再核对其他未授权客户端仍被拒绝。业务只在追加授权后成功时，记录两次 ACL 语义及 Operator 的鉴权结果；将包/业务 Operator 的认证兼容性与 OpenSaola 的状态投影分别归因。完成后按 CLI 路径删除实例、MO 和包，核对本轮保留卷及集群清理。[Kafka 真实发布入口](field-workload-tests_zh.md)

### Kafka 滚动后的 exporter

包启用 kafka-exporter 时，在最后一个 broker Ready 后、删除 MID 前，分别记录 broker 和 exporter 的 Ready、restartCount、最后一次重启原因及采样时间。通过包实际声明的 Service 和 metrics 端口请求 `/metrics`，验证能读到该实例的 broker 或 topic 指标。达到约定观察时限仍未恢复时，将监控验收记为失败，另列 SCRAM 生产消费的结果。
