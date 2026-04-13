[English](troubleshooting.md) | **中文**

# 故障排查指南

本指南帮助诊断和解决 OpenSaola 中间件管理中的常见问题。有关详细的架构和 reconcile 流程信息，请参阅[技术文档](opensaola-technical.md)。

## 目录

- [资源状态](#资源状态)
- [常见问题](#常见问题)
  - [Middleware 卡在 "Unavailable" 状态](#middleware-卡在-unavailable-状态)
  - ["no matching MiddlewareOperator found"](#no-matching-middlewareoperator-found)
  - ["package not ready" / "package install failed"](#package-not-ready--package-install-failed)
  - [Middleware 卡在 "Updating" 状态](#middleware-卡在-updating-状态)
  - [Finalizer 阻止删除](#finalizer-阻止删除)
  - [Pod 卡在 CrashLoopBackOff](#pod-卡在-crashloopbackoff)
  - [CustomResource 删除后自动重建](#customresource-删除后自动重建)
  - [MiddlewareAction 未执行](#middlewareaction-未执行)
- [调试命令](#调试命令)
- [检查 Conditions](#检查-conditions)
- [日志配置](#日志配置)
- [延伸阅读](#延伸阅读)

---

## 资源状态

每个 OpenSaola CRD 都有一个 `status.state` 字段。可能的三个值如下：

| 状态 | 含义 | 出现时机 |
|------|------|---------|
| `Available` | 资源健康且完全可用。 | 所有 status conditions 均为 `True`。 |
| `Unavailable` | 资源遇到错误或尚未就绪。 | 任一 status condition 为 `False`。对于 MiddlewareAction，`Unknown` 状态也会触发此状态。 |
| `Updating` | 资源正在升级到新的 package 版本。 | `middleware.cn/update` annotation 已设置且升级流程正在进行中。 |

### Phase 值

Middleware 上的 `status.customResources.phase` 字段反映底层自定义资源的阶段：

| Phase | 含义 |
|-------|------|
| `""` (空) | 未知 / 初始状态 |
| `Checking` | 资源正在验证中 |
| `Checked` | 验证完成 |
| `Creating` | 资源正在创建中 |
| `Updating` | 资源正在更新中 |
| `Running` | 资源正常运行 |
| `Failed` | 资源遇到错误 |
| `UpdatingCustomResources` | 自定义资源正在更新中 |
| `BuildingRBAC` | RBAC 资源正在创建中 |
| `BuildingDeployment` | Deployment 正在创建中 |
| `Finished` | 生命周期操作已完成 |
| `MappingFields` | CUE 字段映射进行中 |
| `Executing` | Action 正在执行中 |

---

## 常见问题

### Middleware 卡在 "Unavailable" 状态

**症状**：Middleware 资源显示 `State: Unavailable`，且无法转换到 `Available`。

**原因**：
1. 一个或多个 status conditions 的 `Status: False`
2. 引用的 MiddlewareBaseline 不存在或未处于 `Available` 状态
3. MiddlewareOperatorBaseline 引用无效
4. 缺少必填的 `necessary` 参数（例如 `image` 是必填项）
5. Baseline 合并期间模板渲染失败
6. Pre-actions 执行失败

**调试步骤**：

```bash
# 1. 检查 Middleware 的状态和 conditions
kubectl get mid <name> -n <namespace> -o yaml

# 2. 找到第一个 False condition —— 其 message 说明了根本原因
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.status == "False")'

# 3. 检查 status reason 字段获取摘要
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.reason}'

# 4. 确认引用的 baseline 存在且为 Available
kubectl get mb <baseline-name> -o jsonpath='{.status.state}'

# 5. 检查 operator 日志获取详细错误信息
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=100
```

**解决方案**：根据 `False` condition 指示的根本原因进行处理。常见修复方法包括：
- 确保引用的 MiddlewareBaseline 和 MiddlewareOperatorBaseline 存在
- 提供所有必填的 `necessary` 字段（至少必须指定 `image`）
- 修复参数或配置中的模板语法错误
- 验证 pre-action 的 MiddlewareActionBaseline 资源存在

---

### "no matching MiddlewareOperator found"

**症状**：Middleware 为 `Unavailable`，conditions 或 operator 日志显示找不到匹配的 MiddlewareOperator。

**原因**：
1. 目标命名空间中尚未创建 MiddlewareOperator 资源
2. MiddlewareOperator 的 baseline 引用与 Middleware 期望的 GVK 不匹配
3. MiddlewareOperatorBaseline 的 `gvks` 列表中不包含所需的 GVK 条目

**调试步骤**：

```bash
# 1. 列出命名空间中所有 MiddlewareOperator
kubectl get mo -n <namespace>

# 2. 检查 MiddlewareOperator 的 baseline 及其 GVK
kubectl get mob <operator-baseline-name> -o jsonpath='{.spec.gvks}'

# 3. 与 Middleware 的 operatorBaseline.gvkName 进行对比
kubectl get mid <name> -n <namespace> -o jsonpath='{.spec.operatorBaseline}'
```

**解决方案**：
- 在相同命名空间中创建所需的 MiddlewareOperator
- 确保其 `spec.baseline` 指向有效的 MiddlewareOperatorBaseline
- 验证 MiddlewareOperatorBaseline 的 `spec.gvks` 列表中包含 `name` 与 Middleware 的 `operatorBaseline.gvkName` 匹配的条目

---

### "package not ready" / "package install failed"

**症状**：MiddlewarePackage 未处于 `Available` 状态，或 baselines/configurations 未被创建。

**原因**：
1. Package Secret 没有必需的标签 `middleware.cn/project: OpenSaola`
2. Secret 数据损坏或 tar/zstd 压缩包格式错误
3. Secret 缺少 `middleware.cn/install` annotation
4. Secret 所在命名空间不正确（应与 `config.dataNamespace` 一致，默认为 `middleware-operator`）
5. Package 内的 CRD 文件包含无效定义

**调试步骤**：

```bash
# 1. 检查 Secret 是否存在且带有正确的标签
kubectl get secret -n <data-namespace> -l middleware.cn/project=OpenSaola

# 2. 检查 MiddlewarePackage 是否已创建
kubectl get mp

# 3. 检查 MiddlewarePackage 的状态和 conditions
kubectl get mp <package-name> -o yaml

# 4. 检查 Secret annotations 中的安装错误
kubectl get secret <secret-name> -n <data-namespace> -o jsonpath='{.metadata.annotations}'

# 5. 验证 middleware.cn/enabled 标签为 "true"
kubectl get secret <secret-name> -n <data-namespace> -o jsonpath='{.metadata.labels.middleware\.cn/enabled}'

# 6. 检查 operator 日志中的 package 解析错误
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=100 | grep -i "package"
```

**解决方案**：
- 确保 Secret 带有标签 `middleware.cn/project: OpenSaola`
- 确保 Secret 位于配置的 `dataNamespace` 中（检查 operator 的 `config.yaml` 或 Helm values 中的 `config.dataNamespace`）
- 如果压缩包损坏，使用 `saola-cli` 重新上传 package
- 检查 Secret 上的 `middleware.cn/installError` annotation 获取具体错误详情
- 检查 `middleware.cn/installDigest` annotation 验证 package 摘要

---

### Middleware 卡在 "Updating" 状态

**症状**：Middleware 显示 `State: Updating`，且无法转换到 `Available`。

**原因**：
1. 升级目标的 package 版本不可用
2. `middleware.cn/baseline` annotation 引用的新 baseline 不存在
3. `ReplacePackage` 流程中发生错误
4. 新 package 的模板渲染或参数合并失败

**调试步骤**：

```bash
# 1. 检查 update annotation
kubectl get mid <name> -n <namespace> -o jsonpath='{.metadata.annotations.middleware\.cn/update}'

# 2. 检查 baseline annotation
kubectl get mid <name> -n <namespace> -o jsonpath='{.metadata.annotations.middleware\.cn/baseline}'

# 3. 验证目标 package 版本是否存在
kubectl get mp -l middleware.cn/packageversion=<target-version>

# 4. 检查 Updating condition
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.type == "Updating")'

# 5. 检查 operator 日志
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200 | grep -i "update\|replace"
```

**解决方案**：
- 确保目标 package 版本已发布且对应的 MiddlewareBaseline 存在
- 如果升级卡住需要中止，移除 update annotation：
  ```bash
  kubectl annotate mid <name> -n <namespace> middleware.cn/update- middleware.cn/baseline-
  ```
  注意：移除这些 annotation 不会回滚已应用的更改。你可能需要手动恢复之前的状态。

---

### Finalizer 阻止删除

**症状**：Middleware 或 MiddlewareOperator 资源已设置 `deletionTimestamp`，但未被移除。资源一直处于 `Terminating` 状态。

**背景**：OpenSaola 使用 finalizer 确保在删除 Middleware 或 MiddlewareOperator 之前正确清理依赖资源：
- `middleware.cn/middleware-cleanup` -- 用于 Middleware 资源
- `middleware.cn/middlewareoperator-cleanup` -- 用于 MiddlewareOperator 资源

Controller 在首次 reconcile 资源时添加 finalizer，并在清理成功完成后移除。

**原因**：
1. Operator Pod 未运行，因此无法处理 finalizer
2. 清理逻辑遇到错误（例如删除依赖的自定义资源失败）
3. Operator 缺少删除依赖资源的 RBAC 权限
4. 网络或 API Server 问题阻止了清理

**调试步骤**：

```bash
# 1. 检查资源是否有 finalizer
kubectl get mid <name> -n <namespace> -o jsonpath='{.metadata.finalizers}'

# 2. 检查 operator Pod 状态
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>

# 3. 检查 operator 日志中的清理错误
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200 | grep -i "finalizer\|cleanup\|delete"

# 4. 检查依赖资源是否仍然存在
kubectl get all -n <namespace> -l middleware.cn/source=<middleware-name>
```

**解决方案**：
- 如果 operator 正在运行，检查日志中的清理错误并解决底层问题
- 如果 operator 未运行，先启动它并等待 finalizer 被处理
- 作为**最后手段**，手动移除 finalizer（这会跳过清理，可能留下孤立资源）：
  ```bash
  kubectl patch mid <name> -n <namespace> --type=json -p='[{"op": "remove", "path": "/metadata/finalizers"}]'
  ```
  手动移除后，检查并清理任何孤立资源（Deployment、RBAC、ConfigMap 等）

---

### Pod 卡在 CrashLoopBackOff

**症状**：OpenSaola operator Pod 或中间件 operator 的 Deployment Pod 处于 `CrashLoopBackOff` 状态。

**原因**：
1. 配置无效或缺少环境变量
2. 资源不足（OOMKilled）
3. CRD 定义未安装或版本不匹配
4. Operator 的 RBAC 权限不足

**调试步骤**：

```bash
# 1. 检查 Pod 状态和重启次数
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>

# 2. 检查 Pod 事件
kubectl describe pod <pod-name> -n <operator-namespace>

# 3. 检查上次崩溃的日志
kubectl logs <pod-name> -n <operator-namespace> --previous --tail=100

# 4. 检查 Pod 是否被 OOMKilled
kubectl get pod <pod-name> -n <operator-namespace> -o jsonpath='{.status.containerStatuses[0].lastState.terminated.reason}'

# 5. 检查 CRD 安装情况
kubectl get crds | grep middleware.cn
```

**解决方案**：
- 如果是 OOMKilled，在 Helm values 中增加内存限制（`resources.limits.memory`）
- 如果 CRD 缺失，使用 `kubectl.installCRDs: true` 重新安装 Helm chart
- 检查崩溃日志中的具体错误信息并相应处理
- 验证 RBAC 权限配置正确

---

### CustomResource 删除后自动重建

**症状**：你删除了由 Middleware 管理的 CustomResource，但它立即被重新创建。

**原因**：这是预期行为。CR Watcher 检测到删除操作后会检查所属的 Middleware 是否仍然存在。如果 Middleware 存在，CR 会自动重建以维持期望状态。

**解决方案**：
- 要永久移除 CustomResource，请改为删除所属的 Middleware 资源
- 如果需要修改 CR，请更新 Middleware spec 而不是直接编辑 CR

---

### MiddlewareAction 未执行

**症状**：已创建 MiddlewareAction，但没有任何反应。

**原因**：
1. MiddlewareAction 的 `status.state` 已设置（非空）。MiddlewareAction 是一次性资源 -- 一旦有了 state，就不会重新执行
2. `spec.baseline` 引用了不存在的 MiddlewareActionBaseline
3. Baseline 的 `baselineType` 为 `PreAction`，这种类型只在 Middleware/MiddlewareOperator reconcile 流程中执行，不会独立执行

**调试步骤**：

```bash
# 1. 检查当前状态
kubectl get ma <name> -n <namespace> -o jsonpath='{.status.state}'

# 2. 检查引用的 baseline 是否存在
kubectl get mab <baseline-name>

# 3. 检查 baseline 类型
kubectl get mab <baseline-name> -o jsonpath='{.spec.baselineType}'

# 4. 检查 conditions 中的步骤级错误
kubectl get ma <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.'
```

**解决方案**：
- 如果需要重新执行 action，删除现有的 MiddlewareAction 并创建新的
- 确保 baseline 的 `baselineType` 为 `NormalAction`（或 `OpsAction`）以支持独立执行

---

## 调试命令

### 按 CRD 快速参考

| 资源 | 简称 | 作用域 | 列出全部 | 查看详情 | 检查状态 |
|------|------|--------|---------|---------|---------|
| Middleware | `mid` | Namespaced | `kubectl get mid -A` | `kubectl describe mid <name> -n <ns>` | `kubectl get mid <name> -n <ns> -o jsonpath='{.status.state}'` |
| MiddlewareBaseline | `mb` | Cluster | `kubectl get mb` | `kubectl describe mb <name>` | `kubectl get mb <name> -o jsonpath='{.status.state}'` |
| MiddlewareOperator | `mo` | Namespaced | `kubectl get mo -A` | `kubectl describe mo <name> -n <ns>` | `kubectl get mo <name> -n <ns> -o jsonpath='{.status.state}'` |
| MiddlewareOperatorBaseline | `mob` | Cluster | `kubectl get mob` | `kubectl describe mob <name>` | `kubectl get mob <name> -o jsonpath='{.status.state}'` |
| MiddlewarePackage | `mp` | Cluster | `kubectl get mp` | `kubectl describe mp <name>` | `kubectl get mp <name> -o jsonpath='{.status.state}'` |
| MiddlewareAction | `ma` | Namespaced | `kubectl get ma -A` | `kubectl describe ma <name> -n <ns>` | `kubectl get ma <name> -n <ns> -o jsonpath='{.status.state}'` |
| MiddlewareActionBaseline | `mab` | Cluster | `kubectl get mab` | `kubectl describe mab <name>` | `kubectl get mab <name> -o jsonpath='{.status.state}'` |
| MiddlewareConfiguration | `mc` | Cluster | `kubectl get mc` | `kubectl describe mc <name>` | `kubectl get mc <name> -o jsonpath='{.status.state}'` |

### Operator 日志

```bash
# 查看 operator 日志（尾部）
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200

# 实时跟踪 operator 日志
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> -f

# 按特定中间件类型过滤
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=500 | grep "redis"

# 仅过滤错误
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=500 | grep -i "error\|fail"
```

### Package 相关检查

```bash
# 列出所有 package Secret
kubectl get secret -n <data-namespace> -l middleware.cn/project=OpenSaola

# 检查 package Secret 的标签和 annotation
kubectl get secret <name> -n <data-namespace> -o jsonpath='{.metadata.labels}'
kubectl get secret <name> -n <data-namespace> -o jsonpath='{.metadata.annotations}'

# 列出特定 package 发布的所有 baseline
kubectl get mb -l middleware.cn/packagename=<package-name>
kubectl get mob -l middleware.cn/packagename=<package-name>
kubectl get mab -l middleware.cn/packagename=<package-name>
```

---

## 检查 Conditions

Conditions 提供了任何 OpenSaola 资源最详细的诊断信息。每个 condition 代表 reconcile 流程中的一个步骤。

### 读取 conditions

```bash
# 以 JSON 格式获取所有 conditions
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.'

# 仅获取失败的 conditions
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.status == "False")'

# 获取特定类型的 condition
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.type == "Checked")'
```

### Condition 类型及其含义

| Condition 类型 | 适用于 | 检查内容 |
|---------------|--------|---------|
| `Checked` | Middleware, MO, Action | Spec 验证通过 |
| `TemplateParseWithBaseline` | Middleware, MO | Baseline 合并和模板渲染成功 |
| `BuildExtraResource` | Middleware, MO | 额外资源（来自 Configurations）已创建 |
| `ApplyRBAC` | MO | RBAC 资源（ServiceAccount、Role/ClusterRole、Bindings）已应用 |
| `ApplyOperator` | MO | Operator Deployment 已应用 |
| `ApplyCluster` | Middleware | CustomResource 已创建/更新 |
| `MapCueFields` | Action | CUE 字段映射成功 |
| `ExecuteAction` | Action | Action 执行完成 |
| `ExecuteCue` | Action | CUE 步骤已执行 |
| `ExecuteCmd` | Action | 命令步骤已执行 |
| `ExecuteHttp` | Action | HTTP 步骤已执行 |
| `Running` | Middleware, MO | 资源正常运行中 |
| `Updating` | Middleware, MO | 升级流程状态 |

### Condition 状态值

- `True` -- 步骤成功完成
- `False` -- 步骤失败（查看 `message` 了解详情）
- `Unknown` -- 步骤正在初始化（reason: `Initing`）

---

## 日志配置

OpenSaola 的日志可以通过 Helm values 或编辑 operator 的 ConfigMap 进行配置。

### Helm values

```yaml
config:
  # 日志级别: 0=debug, 1=info, 2=warn, 3=error
  logLevel: 0
  # 日志格式: "console"（人类可读）或 "json"（结构化）
  logFormat: "console"
  # 日志文件路径（空字符串禁用文件日志）
  logFilePath: ""
```

### 运行时更改日志级别

更新 ConfigMap 并重启 operator Pod：

```bash
# 编辑 operator ConfigMap
kubectl edit configmap opensaola-config -n <operator-namespace>

# 重启 operator 以应用更改
kubectl rollout restart deployment opensaola -n <operator-namespace>
```

### 故障排查建议日志级别

- **Level 0 (debug)**：最高详细度。用于调查 reconcile 流程问题。显示所有 condition 转换、合并操作和模板渲染细节。
- **Level 1 (info)**：默认级别。显示 reconcile 事件、状态转换和关键操作。
- **Level 2 (warn)**：仅显示警告和错误。系统稳定时在生产环境中使用。
- **Level 3 (error)**：仅显示错误。用于过滤噪音、专注于故障排查时使用。

---

## 延伸阅读

- [技术文档](opensaola-technical.md) -- 架构、CRD 字段参考、reconcile 流程、状态机细节、标签/annotation 约定
- [Package 文档](opensaola-packaging.md) -- Package 格式、baseline 系统、action 系统、配置模板、Redis 案例研究
