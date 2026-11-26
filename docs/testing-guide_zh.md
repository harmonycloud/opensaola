# OpenSaola 测试指南

> English version: [testing-guide.md](testing-guide.md)

本指南介绍如何运行 OpenSaola 项目中各层级的测试，从快速单元测试到在 Kind 集群上的完整端到端验证。

## 选择测试范围

| 场景 | 命令 | 实际执行 |
|---|---|---|
| 日常开发 | `bash scripts/test_all_gates.sh pr` | 默认 Go 测试与竞态、lint、envtest、构建、Helm、本地 Kind 控制器冒烟、Redis 字段与数据、目录命名空间保留 |
| 候选封板 | `make test-release` | 上述检查，envtest 同时开启竞态，再执行真实 Helm 存量升级、六组原包样例和两个主动 fuzz 目标 |
| 性能对照 | `make test-performance` | Go 基准及上一正式版与当前工作树在本机交替执行的 API 写入对照 |
| 全量 | `make test-full` | 封板与性能两组，顺序执行 |

从仓库根目录执行。准备 Go、Python 3、Docker、Kind、kubectl 与 Helm。发布前设置 `MIDDLEWARE_FIXTURES` 为[原包样例目录](middleware-matrix.md)，并准备各包的镜像、依赖和 CLI。每轮都会在 `TEST_REPORT_DIR`（默认系统临时目录）下新建报告目录，每项保留日志和 `summary.tsv` 记录。直接运行脚本时，退出码 0 表示全部通过，1 表示有失败，77 表示存在阻塞且其余项目通过。使用 make 时，查看原始阶段退出码请读汇总表。重跑会生成另一份报告。

Helm 升级默认读取本地最新正式版标签，在独立集群用正式 Chart 创建 Redis 和 PVC，通过默认 hook 升级到候选 Chart。用 `HELM_PREVIOUS_VERSION=1.5.1` 可重测引入 Configuration 写入进度字段的升级。离线包使用 `HELM_PREVIOUS_CHART`，工作树中的控制器变更需提供已构建的 `HELM_CANDIDATE_IMAGE`。

性能比较可设 `PERF_BASE_REF`、`PERF_PAIRS`、`KUBEBUILDER_ASSETS`。每组在独立 envtest 中运行相同探针，交替新旧顺序，记录 1 KiB、8 并发及 64 KiB、32 并发的吞吐和 P95。数据错误、撤回未生效、幂等重放产生写入均失败。对比时间指标时保留原始窗口并避开其他高负载任务。

`make test-envtest` 核对历史行为的实际 PASS 回执。例如 StatefulSet 同时验证“5Gi 遇到默认字段能发布”和“改成 1Gi 被拒绝”。用例更名时同步维护[行为与断言的对应关系](../hack/check-regression-events.py)；缺失或跳过的必测回执会导致失败。

MinIO 降员与双存储类、ZooKeeper 选主和重启 Action、Kafka 严格 ACL 需要特定拓扑和权限。按[中间件专项复测](periodic-runtime-checks_zh.md)在相关改动时执行，单列现场结论。

## 测试层级概览

| 层级 | 命令 | 范围 | 依赖 |
|------|------|------|------|
| 单元测试 | `make test` | 包含普通 controller 测试，排除集群 E2E | 无 |
| 竞态检测 | `make test-race` | 同单元测试，启用 race detector | 无 |
| Envtest | `make test-envtest` | Controller 集成测试 | envtest 二进制文件 |
| E2E | `make test-e2e-smoke` | 在 Kind 上部署 Manager 并测试 | Kind 集群 + Docker |
| 基准测试 | `make bench` | 性能基准测试 | 无 |

## 运行单元测试

```bash
make test
```

运行具备默认构建标签的测试并生成 `cover.out`。带 `envtest` 标签的 API 用例由 `make test-envtest` 单独运行。

运行特定包的测试：

```bash
go test ./internal/cache/... -v
go test ./pkg/tools/... -v -run TestReadTarInfo
```

## 运行集成测试（envtest）

Controller 测试使用 `envtest` 框架，它提供一个本地 API Server 和 etcd，无需完整集群。

```bash
# 首次运行：下载 envtest 所需的 Kubernetes 二进制文件
make setup-envtest

# 运行 controller envtest 测试
make test-envtest
```

## 构建和部署进行手动测试

```bash
# 构建 Docker 镜像
make docker-build IMG=opensaola:v0.1.0

# 安装 CRD 到当前集群
make install

# 部署 operator
make deploy IMG=opensaola:v0.1.0

# 验证
kubectl get pods -n opensaola-system
kubectl logs -n opensaola-system deploy/opensaola-controller-manager --tail=20

# 测试完成后清理
make undeploy
make uninstall
```

## 专属 Kind 集群

`make test-e2e-smoke`、`make test-e2e` 和 `scripts/e2e-full-test.sh` 都在新建的本地 Kind 集群运行控制器冒烟测试。每轮使用独立 kubeconfig，结束后删除本轮集群。无需切换 kubectl 上下文或手动建群。

## 代码覆盖率

```bash
# 运行测试并在浏览器中打开 HTML 覆盖率报告
make coverage
```

此命令会先运行 `make test`，然后以交互式 HTML 报告的形式打开 `cover.out`。

## 基准测试

```bash
make bench
```

比较两次基准测试的结果：

```bash
make bench > /tmp/old.txt
# ... 修改代码 ...
make bench > /tmp/new.txt
make benchstat BENCH_OLD=/tmp/old.txt BENCH_NEW=/tmp/new.txt
```

## 使用 saola-cli 进行 E2E 验证

[saola-cli](https://gitee.com/opensaola/saola-cli) 项目提供了一个 CLI 工具，可用于端到端验证 operator 的行为。

### 前置条件
- 已构建 saola-cli 二进制文件：`cd ../saola-cli && make build`
- 一个中间件包目录（例如 `../dataservice-baseline/clickhouse`）

### 运行 saola-cli E2E
部署 operator 之后（参见上方"构建和部署进行手动测试"），使用 saola-cli 的 E2E 脚本：

```bash
cd ../saola-cli
PKG_DIR=../dataservice-baseline/clickhouse ./scripts/e2e-test.sh
```

此脚本覆盖：包安装 → Baseline 查询 → Operator 部署 → Middleware 部署 → 输出格式验证。

ClickHouse 的示例 YAML 文件位于 `saola-cli/docs/e2e-samples/`。

## 生命周期测试场景

以下手动测试场景用于验证 operator 在完整资源生命周期中的行为。请在部署 operator 之后运行（参见"构建和部署进行手动测试"）。

### 删除生命周期
1. 创建 Middleware：`kubectl apply -f <middleware.yaml>`
2. 等待 Available 状态：`kubectl get mid -n <ns> -w`
3. 删除：`kubectl delete mid <name> -n <ns>`
4. 验证 finalizer 执行：`kubectl get mid <name> -n <ns>`（应短暂显示 Terminating）
5. 验证 Pod 终止：`kubectl get pods -n <ns>`（Middleware Pod 应消失）
6. 验证子 CR 被清除：`kubectl get chi -n <ns>`（ClickHouseInstallation 应已删除）

### 升级生命周期
1. 创建版本为 24.8 的 Middleware
2. 添加注解触发升级：`kubectl annotate mid <name> -n <ns> middleware.cn/update=<target-version> middleware.cn/baseline=<target-baseline>`
3. 验证状态流转：Available → Updating → Available
4. 验证 Pod 已使用新配置更新

### 错误恢复
1. 创建引用不存在 Baseline 的 Middleware：`spec.baseline: "nonexistent-baseline"`
2. 验证 Unavailable 状态及错误条件：`kubectl get mid <name> -n <ns> -o jsonpath='{.status}'`
3. 修复 Baseline 引用：`kubectl edit mid <name> -n <ns>`
4. 验证自动恢复至 Available

### Operator 重启恢复
1. 创建 Middleware，等待 Available
2. 杀掉 operator Pod：`kubectl delete pod -n opensaola-system -l control-plane=controller-manager`
3. 等待新 Pod 启动：`kubectl get pods -n opensaola-system -w`
4. 验证 Middleware 状态不变：`kubectl get mid -n <ns>`

## CI 检查（推送前在本地运行）

```bash
make lint                                      # golangci-lint
make test                                      # 单元测试
make manifests generate && git diff --exit-code # CRD 漂移检查
```
