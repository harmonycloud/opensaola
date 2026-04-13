# OpenSaola 测试指南

> English version: [testing-guide.md](testing-guide.md)

本指南介绍如何运行 OpenSaola 项目中各层级的测试，从快速单元测试到在 Kind 集群上的完整端到端验证。

## 测试层级概览

| 层级 | 命令 | 范围 | 依赖 |
|------|------|------|------|
| 单元测试 | `make test` | 除 controller 和 e2e 外的所有包 | 无 |
| 竞态检测 | `make test-race` | 同单元测试，启用 race detector | 无 |
| Envtest | `make test-envtest` | Controller 集成测试 | envtest 二进制文件 |
| E2E | `make test-e2e-smoke` | 在 Kind 上部署 Manager 并测试 | Kind 集群 + Docker |
| 基准测试 | `make bench` | 性能基准测试 | 无 |

## 运行单元测试

```bash
make test
```

此命令会运行除 `internal/controller`（需要 envtest）和 `test/e2e`（需要集群）之外的所有包。会自动生成 `cover.out` 覆盖率文件。

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

## E2E 测试

### 自动模式（自动创建和销毁 Kind 集群）

```bash
make test-e2e-smoke
```

此目标会创建一个名为 `opensaola-e2e` 的临时 Kind 集群，运行 e2e 测试套件，然后删除集群。

### 使用已有集群

```bash
# 创建 Kind 集群（如果没有的话）
make kind-create

# 运行 e2e 测试
make test-e2e

# 清理
make kind-delete
```

### 完整 E2E 脚本

提供了一个便捷脚本，可以一键完成构建、部署和验证 operator 的端到端流程：

```bash
./scripts/e2e-full-test.sh

# 覆盖镜像标签：
IMG=opensaola:dev ./scripts/e2e-full-test.sh
```

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

## CI 检查（推送前在本地运行）

```bash
make lint                                      # golangci-lint
make test                                      # 单元测试
make manifests generate && git diff --exit-code # CRD 漂移检查
```

## 包覆盖率矩阵

**已有测试** 的包：

| 包 | 测试文件数 |
|----|-----------|
| `internal/controller` | 12 |
| `pkg/tools` | 6 |
| `internal/k8s` | 4 |
| `internal/service/packages` | 3 |
| `internal/service/middlewareoperator` | 3 |
| `test/e2e` | 2 |
| `api/v1` | 1 |
| `internal/cache` | 1 |
| `internal/concurrency` | 1 |
| `internal/k8s/kubeclient` | 1 |
| `internal/resource` | 1 |
| `internal/resource/logger` | 1 |
| `internal/service/consts` | 1 |
| `internal/service/customresource` | 1 |
| `internal/service/middleware` | 1 |
| `internal/service/middlewareaction` | 1 |
| `internal/service/middlewareactionbaseline` | 1 |
| `internal/service/middlewarebaseline` | 1 |
| `internal/service/middlewareconfiguration` | 1 |
| `internal/service/middlewareoperatorbaseline` | 1 |
| `internal/service/middlewarepackage` | 1 |
| `internal/service/status` | 1 |
| `internal/service/synchronizer` | 1 |
| `internal/service/watcher` | 1 |
| `pkg/config` | 1 |
| `pkg/metrics` | 1 |
| `pkg/tools/ctxkeys` | 1 |

**尚无测试** 的包：

| 包 | 备注 |
|----|------|
| `cmd` | 主入口；通过 e2e 测试覆盖 |
| `test/utils` | 测试工具包（非可测试包） |
