# 字段管理的工作负载回归

`make test` 和 `make test-race` 包含 controller 的普通单测。运行 `make test-envtest` 会在独立 API Server 上执行包含 Action 在内的 internal 测试。PR 的 Kind smoke 完成后另起一个临时集群运行 Redis 字段集成用例。

## Redis

本机具备 Go、Docker、Kind 和 kubectl 时执行 `make test-fields-redis`。脚本运行 `TestLocalIntentReconcile` 的完整场景，包括已有 Deployment 接管、挂载及 PVC 跨滚动读写。脚本默认使用 `kindest/node:v1.34.3`，加载 `redis:7.2.7-alpine` 用于数据回归。可用 `KIND_NODE_IMAGE` 指定其他节点镜像，`KIND_BIN` 指定本机 Kind 可执行文件。

## Kafka 首装与正式版本升级

在具备原包和匹配镜像的交付环境执行。输入为原始 Kafka 包目录、saola CLI，以及对应基线的以下测试材料。脚本从当前源码编译候选 manager 并在独立 Kind 上运行。退出时清理临时集群和 manager。

| 输入 | 内容 |
| --- | --- |
| `KAFKA_PACKAGE_DIR` | 包目录，内含原始 `metadata.yaml` |
| `KAFKA_FIXTURES/mo.json`、`mid.json` | 同一测试命名空间内的有效 MO 和单 broker MID。直接使用该包的实际 baseline、版本、镜像和依赖参数，预留名为 `log` 的新增卷和挂载 |
| `KAFKA_FIXTURES/dependencies.yaml` | 为本次集群准备的 ZooKeeper Service 和 Deployment 等依赖，MID 中的 ZooKeeper 地址与部署的 Service 对应 |
| `KAFKA_FIXTURES/images/*.tar` | 离线运行可放入带原始标签、适用平台的 OCI/Docker 镜像归档。在线运行时节点要能按原名拉取所需镜像 |
| `SAOLA_BIN` | 匹配原包的 CLI，默认使用 PATH 中的 `saola` |
| `KAFKA_PREVIOUS_MANAGER` | 升级测试所用旧控制器二进制的绝对路径，留空执行当前控制器首装测试 |

测试针对 `statefulsetComponent.broker.spec.template.spec`、`<MID>-broker-0` 和 `<MID>-headless-svc:9092` 的 Kafka 3.9.1 包约定，使用 broker 的 SCRAM-SHA-256 实际收发消息。配置材料包含测试凭据时存放在本地私有目录。

```bash
export KAFKA_PACKAGE_DIR=/path/to/original-kafka-package
export KAFKA_FIXTURES=/path/to/kafka-test-fixtures
make test-fields-kafka

# 正式版接续：可执行文件由指定正式 tag 构建。
export KAFKA_PREVIOUS_MANAGER=/path/to/v1.4.0/manager
make test-fields-kafka
```

两种模式均等待 MID 的当前 generation 为 Available、broker 为 Ready，并在 `/root/logs` 实际写入、读回和删除文件，并向临时 topic 生产消息。升级模式额外等待旧版写好不可变历史记录、停止旧控制器、启动候选控制器，触发一轮保留挂载声明的正常 MID 更新，检验历史已迁移。随后撤销挂载，验证另一 Ready broker 的内核挂载表和跨滚动的旧消息；MID 与 Kafka CR 的 UID 应保持一致。最后在已卸载的 Ready 实例上重新用 MID 新增同一份挂载，等待新的 Ready Pod 并再次读写文件。有原包和对应镜像的交付环境可在正式发布前运行此流程。

旧 v1 历史按当时的列表规则处理删除。对于旧记录作为整段保存的列表，旧记录无法证明内层某个命名成员归属时，首次迁移会保留该成员。本流程覆盖先正常更新建立新历史、再撤销的顺序。
