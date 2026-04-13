[English](CONTRIBUTING.md) | **中文**

# 贡献指南

感谢你对 OpenSaola 项目的关注！本文档提供了贡献指南、开发流程和架构背景，帮助你快速上手。

## 快速开始

### 前置条件

- **Go 1.26.1+**（参见 `go.mod`）
- **Docker**（或其他 OCI 兼容的容器工具）
- **kubectl**
- **Kind**（用于本地集群和 e2e 测试）
- **Helm 3**

### 克隆和构建

```bash
git clone https://github.com/OpenSaola/opensaola.git
cd opensaola
make build
```

此命令会生成 CRD、运行代码生成、格式化、vet 检查，并编译 manager 二进制文件到 `bin/manager`。

### 运行单元测试

```bash
make test
```

> **注意：** `make test` 排除了 controller envtest 测试（`internal/controller/`）和 e2e 测试（`test/e2e/`）。它会运行所有其他包并生成覆盖率报告。

### 运行 Race Detector 测试

```bash
make test-race
```

与 `make test` 范围相同，但启用了 Go 的竞态检测器并禁用测试缓存（`-count=1`）。

### 运行集成测试（envtest）

Controller 测试使用 [envtest](https://book.kubebuilder.io/reference/envtest.html) 在进程内启动一个轻量级的 K8s API Server。

```bash
make setup-envtest    # 下载 K8s API Server 二进制文件
make test-envtest     # 运行 controller envtest 测试
```

### 本地运行（Kind 集群）

```bash
make kind-create      # 创建 Kind 集群（默认名称：opensaola-e2e）
make install          # 安装 CRD 到集群
make run              # 本地运行 controller
```

### 运行 E2E 测试

**方式 A — 冒烟测试（自动创建和销毁临时 Kind 集群）：**

```bash
make docker-build IMG=controller:latest
kind load docker-image controller:latest --name opensaola-e2e
make test-e2e-smoke
```

**方式 B — 在已有的 Kind 集群上运行：**

```bash
make kind-create
make docker-build IMG=controller:latest
kind load docker-image controller:latest --name opensaola-e2e
make test-e2e
```

## 项目架构

OpenSaola 是一个 Kubernetes Operator，用于管理中间件生命周期。采用三层架构设计。

### Controller 层（`internal/controller/`）

每个 CRD 有一个专门的 controller 文件，负责：

- Reconcile 循环入口
- Finalizer 管理
- Status 状态收敛
- 基于 Predicate 的事件过滤
- Metrics 指标采集

项目中的 Controller：

| Controller | CRD |
|---|---|
| `middleware_controller.go` | Middleware |
| `middlewareoperator_controller.go` | MiddlewareOperator |
| `middlewareoperator_runtime_controller.go` | MiddlewareOperator（运行时 reconcile） |
| `middlewareaction_controller.go` | MiddlewareAction |
| `middlewarepackage_controller.go` | MiddlewarePackage |
| `middlewarebaseline_controller.go` | MiddlewareBaseline |
| `middlewareoperatorbaseline_controller.go` | MiddlewareOperatorBaseline |
| `middlewareactionbaseline_controller.go` | MiddlewareActionBaseline |
| `middlewareconfiguration_controller.go` | MiddlewareConfiguration |

### Service 层（`internal/service/`）

按子目录组织的各 CRD 业务逻辑：

- **middleware/** — Middleware 实例生命周期（安装、更新、删除、升级）
- **middlewareoperator/** — Operator 部署管理
- **middlewareaction/** — Action 执行（CUE、HTTP、CMD）
- **middlewarepackage/** — Package 资源 reconcile
- **packages/** — Package TAR 解析和缓存
- **middlewarebaseline/** — Baseline 模板管理
- **middlewareoperatorbaseline/** — Operator Baseline 管理
- **middlewareactionbaseline/** — Action Baseline 管理
- **middlewareconfiguration/** — Configuration 资源处理
- **synchronizer/** — 资源间状态同步
- **watcher/** — 自定义资源监听和事件处理
- **customresource/** — 通用自定义资源工具
- **status/** — Status 和 Condition 辅助函数
- **consts/** — 共享常量

### K8s 客户端层（`internal/k8s/`）

所有资源类型的 Kubernetes API 封装。提供带缓存的类型化访问器，覆盖 Deployment、StatefulSet、DaemonSet、Secret、Service、PVC、Pod、RBAC 资源以及所有 OpenSaola 自定义资源。

### CRD 类型（`api/v1/`）

所有 CRD 类型定义：

- `middleware_types.go`、`middlewareoperator_types.go`、`middlewareaction_types.go`
- `middlewarepackage_types.go`、`middlewarebaseline_types.go`
- `middlewareoperatorbaseline_types.go`、`middlewareactionbaseline_types.go`
- `middlewareconfiguration_types.go`
- `labels.go` — 共享 Label 键
- `common.go` — 共享类型辅助定义

## 可用的 Make 命令

### 通用

| 命令 | 说明 |
|---|---|
| `make help` | 显示所有命令及说明 |

### 开发

| 命令 | 说明 |
|---|---|
| `make manifests` | 生成 CRD Manifest、RBAC 和 Webhook 配置 |
| `make generate` | 生成 DeepCopy 方法实现 |
| `make generate-all` | 生成代码 + Manifest + 同步 CRD 到 Helm Chart |
| `make fmt` | 运行 `go fmt` |
| `make vet` | 运行 `go vet` |
| `make test` | 运行单元测试（排除 controller envtest 和 e2e） |
| `make test-race` | 运行单元测试（启用竞态检测） |
| `make test-envtest` | 运行 controller envtest 集成测试 |
| `make coverage` | 运行测试并在浏览器中打开覆盖率报告 |
| `make bench` | 运行 Go 基准测试 |
| `make test-e2e` | 在已有 Kind 集群上运行 e2e 测试 |
| `make test-e2e-smoke` | 在临时 Kind 集群上运行 e2e 冒烟测试 |
| `make kind-create` | 创建 Kind 集群 |
| `make kind-delete` | 删除 Kind 集群 |
| `make lint` | 运行 golangci-lint |
| `make lint-fix` | 运行 golangci-lint 并自动修复 |

### 构建

| 命令 | 说明 |
|---|---|
| `make build` | 编译 manager 二进制文件到 `bin/manager` |
| `make run` | 从源码本地运行 controller |
| `make docker-build` | 构建 Docker 镜像 |
| `make docker-push` | 推送 Docker 镜像 |
| `make docker-buildx` | 构建并推送多架构 Docker 镜像 |

### 部署

| 命令 | 说明 |
|---|---|
| `make install` | 安装 CRD 到当前集群 |
| `make uninstall` | 卸载 CRD |
| `make deploy` | 部署 controller 到当前集群 |
| `make undeploy` | 移除 controller |

## 代码规范

- 提交前运行 `go fmt ./...` 和 `go vet ./...`。`make lint` 运行完整的 golangci-lint 检查。
- 使用 `log.FromContext(ctx)` 进行**结构化日志**记录。不要使用 `fmt.Print` 或标准 `log` 包。
- 错误包装格式：`fmt.Errorf("failed to <动词> <名词>: %w", err)`。
- 保持函数签名与同包中已有模式一致。
- 优先使用表驱动测试。
- CRD 类型变更后需运行 `make manifests generate` 重新生成派生文件。

## Commit 规范

使用 Conventional Commits 格式：

- `feat`：新功能
- `fix`：Bug 修复
- `docs`：文档变更
- `refactor`：代码重构（无行为变更）
- `test`：添加或更新测试
- `chore`：维护、依赖更新

示例：

```
feat: add package cache feature gate
fix: prevent duplicate status updates in MiddlewareAction controller
test: add envtest coverage for finalizer cleanup
```

## Pull Request 流程

1. **Fork 并创建分支** — 从 `main` 分支创建功能分支。
2. **修改代码** — 保持提交聚焦和原子化。
3. **按需重新生成** — 如果修改了 `api/v1/` 中的 CRD 类型，运行：
   ```bash
   make manifests generate
   ```
   将生成的变更与源码变更一起提交。
4. **运行测试** — 至少运行：
   ```bash
   make test           # 单元测试
   make lint           # 代码检查
   ```
   如果修改了 controller 逻辑，还需运行：
   ```bash
   make test-envtest   # controller 集成测试
   ```
5. **更新文档** — 如有行为变更，更新相关文档。
6. **提交 PR** — 清晰描述修改了什么以及为什么。
7. **请求审查** — 标记相关审查者。

## 运行特定测试

```bash
# 按名称运行特定测试
go test ./internal/service/packages/... -v -run TestGetPackage

# 运行 controller envtest 测试
go test ./internal/controller/... -tags=envtest -v

# 运行特定包的基准测试
go test ./internal/service/packages/... -run '^$' -bench . -benchmem

# 运行测试（详细输出，禁用缓存）
go test ./internal/service/middleware/... -v -count=1
```

## 报告问题

请使用 GitHub Issues 并添加适当的标签。报告 Bug 时请包含：

- OpenSaola 版本或 commit hash
- Kubernetes 版本
- 复现步骤
- 预期行为 vs 实际行为
- 相关日志（controller 日志、`kubectl describe` 输出）

## 许可证

参与贡献即表示你同意你的贡献将以 Apache License 2.0 许可证授权。
