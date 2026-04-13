# OpenSaola

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://img.shields.io/github/actions/workflow/status/OpenSaola/opensaola/ci.yml?branch=main&label=CI)](https://github.com/OpenSaola/opensaola/actions)

[English](README.md) | **中文**

基于 Kubernetes Operator 模式的**中间件全生命周期管理平台**。以声明式方式安装、升级、配置、监控和删除 Kubernetes 集群上的中间件实例（Redis、MySQL、Kafka 等）。

## 为什么选择 OpenSaola？

在 Kubernetes 上管理中间件通常需要为每种中间件部署独立的 Operator，各自有不同的 API、升级路径和运维方式。OpenSaola 提供了一个**统一的、包驱动的框架**，标准化各类中间件的运维操作：

- **统一 API** -- 通过相同的 CRD 模型管理 Redis、MySQL、Kafka 等所有中间件
- **包驱动** -- 中间件能力以可移植的包形式分发，不绑定在 Operator 代码中
- **基线模板** -- 通过可复用的默认 Spec 模板，强制执行组织标准
- **可插拔操作** -- 将生命周期步骤（安装、升级、备份、恢复）定义为 CUE/Go/Shell 脚本
- **独立配置管理** -- 独立于主 Spec 应用配置变更，无需触发完整的 Reconcile 周期

## 工作原理

OpenSaola 是一个**框架** -- 它本身不包含任何中间件能力。通过安装**中间件包**来教会它如何管理特定的中间件类型。

```
                    MiddlewarePackage
              (Secret with TAR archive)
   baselines / templates / actions / configs
                         |
                       install
          +--------------+--------------+---------------+
          |              |              |               |
          v              v              v               v
     Middleware     Operator       Action         Configuration
      Baseline      Baseline      Baseline         (templates)
          |              |
          v              v
      Middleware   MiddlewareOperator ---> Deployment
      (instance)    (manages a type)      (operator pod)
```

**核心流程：**

1. **打包** -- 将中间件定义（基线 + 模板 + 操作脚本）打包为 TAR 归档，存储为 Kubernetes Secret
2. **创建 MiddlewarePackage** -- Operator 自动解包，发布 Baseline、Configuration、Action 等资源
3. **创建 MiddlewareOperator** -- 部署该中间件类型的 Operator（如 Redis Operator）
4. **创建 Middleware** -- 使用基线 + 用户参数创建实际的中间件实例

每个资源通过状态机驱动（`Checking → Creating → Running`），通过标准 Kubernetes Conditions 报告进度。

## CRD 概览

| CRD | 缩写 | 作用域 | 用途 |
|-----|------|--------|------|
| **Middleware** | `mid` | Namespace | 中间件实例（Redis、MySQL 等） |
| **MiddlewareOperator** | `mo` | Namespace | 管理某类中间件的 Operator 部署 |
| **MiddlewareBaseline** | `mb` | Cluster | 中间件默认 Spec 模板 |
| **MiddlewareOperatorBaseline** | `mob` | Cluster | Operator 默认 Spec 模板 |
| **MiddlewareAction** | `ma` | Namespace | 生命周期操作执行（安装、升级、备份等） |
| **MiddlewareActionBaseline** | `mab` | Cluster | 可复用的操作步骤定义 |
| **MiddlewareConfiguration** | `mcf` | Cluster | 运行时配置模板 |
| **MiddlewarePackage** | `mp` | Cluster | 中间件分发包 |

## 快速开始

### 前置条件

- Kubernetes 1.21+
- Helm 3
- kubectl

### 安装

```bash
helm install opensaola chart/opensaola \
  --namespace opensaola-system \
  --create-namespace
```

### 验证

```bash
# 检查 Operator 是否运行
kubectl get pods -n opensaola-system

# 检查 CRD 是否安装
kubectl get crds | grep middleware.cn

# 查看所有中间件实例
kubectl get mid -A
```

### 部署中间件

> **注意：** OpenSaola 需要中间件包才能部署实际的中间件。
> 包中包含定义各中间件类型管理方式的模板和操作脚本。
> 详见[文档](docs/)了解如何创建和安装中间件包。

```bash
# 应用中间件包（存储为 Secret）
kubectl apply -f my-redis-package.yaml

# 创建中间件类型的 Operator
kubectl apply -f my-redis-operator.yaml

# 创建中间件实例
kubectl apply -f my-redis-instance.yaml

# 观察状态收敛
kubectl get mid my-redis -w
```

## 配置

OpenSaola 通过 Helm Values 配置，主要参数：

```yaml
# Operator 配置
config:
  dataNamespace: "middleware-operator"  # 中间件包 Secret 所在命名空间
  logLevel: 0                          # 日志级别：0=debug, 1=info, 2=warn, 3=error
  logFormat: "console"                 # 日志格式："console"（开发）或 "json"（生产）

# 资源配额
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi

# 监控
serviceMonitor:
  enabled: false    # 启用后 Prometheus 自动发现
```

完整配置项参见 [`chart/opensaola/values.yaml`](chart/opensaola/values.yaml)。

## 开发

```bash
# 构建
make build

# 运行测试
make test                # 单元测试
make test-envtest        # Controller 集成测试（envtest）
make test-e2e-smoke      # 端到端测试（自动创建 Kind 集群）

# 代码生成（修改 api/v1/ 类型后）
make generate-all        # 生成代码 + CRD Manifests + 同步到 Helm Chart

# 本地运行
make kind-create         # 创建 Kind 集群
make install             # 安装 CRD
make run                 # 本地运行 Operator

# 代码检查
make lint
```

运行 `make help` 查看所有可用命令。

## 文档

| 文档 | 说明 |
|------|------|
| [技术文档](docs/opensaola-technical.md) | 架构设计、CRD 字段参考、Reconcile 流程、状态机 |
| [包适配文档](docs/opensaola-packaging.md) | 包格式、基线体系、操作系统、CUE 模板、Redis 完整案例 |
| [故障排查指南](docs/troubleshooting_zh.md) | 常见问题、调试命令、日志配置 |
| [升级指南](docs/upgrade-guide_zh.md) | 版本升级流程与回滚 |
| [贡献指南](CONTRIBUTING.md) | 开发环境搭建、架构说明、编码规范 |

## 参与贡献

欢迎贡献！请参阅 [CONTRIBUTING.md](CONTRIBUTING.md) 了解开发环境搭建、项目架构和贡献流程。

## 许可证

Copyright 2025 The OpenSaola Authors.

基于 Apache License 2.0 许可证开源。详见 [LICENSE](LICENSE)。
