[English](upgrade-guide.md) | **中文**

# 升级指南

本指南介绍如何升级 OpenSaola operator 和中间件软件包。有关详细的架构信息，请参阅[技术文档](opensaola-technical.md)。

## 目录

- [升级前检查清单](#升级前检查清单)
- [升级 OpenSaola Operator](#升级-opensaola-operator)
- [升级中间件软件包](#升级中间件软件包)
- [升级后验证](#升级后验证)
- [回滚流程](#回滚流程)
- [CRD 兼容性说明](#crd-兼容性说明)
- [升级故障排查](#升级故障排查)

---

## 升级前检查清单

在升级之前，请完成以下步骤：

### 1. 备份所有自定义资源

```bash
# Back up all OpenSaola CRs
kubectl get mid -A -o yaml > middleware-backup.yaml
kubectl get mo -A -o yaml > middlewareoperator-backup.yaml
kubectl get mb -o yaml > middlewarebaseline-backup.yaml
kubectl get mob -o yaml > middlewareoperatorbaseline-backup.yaml
kubectl get ma -A -o yaml > middlewareaction-backup.yaml
kubectl get mab -o yaml > middlewareactionbaseline-backup.yaml
kubectl get mp -o yaml > middlewarepackage-backup.yaml
kubectl get mc -o yaml > middlewareconfiguration-backup.yaml
```

### 2. 检查当前版本

```bash
# Check the running operator version
helm list -n <operator-namespace>

# Check the operator pod image version
kubectl get deployment opensaola -n <operator-namespace> -o jsonpath='{.spec.template.spec.containers[0].image}'
```

### 3. 查看 CHANGELOG 中的破坏性变更

检查项目的发布说明 [https://github.com/OpenSaola/opensaola/releases](https://github.com/OpenSaola/opensaola/releases)，关注以下内容：
- CRD 字段的新增、移除或重命名
- Label 或 Annotation 约定的变更
- Reconcile 流程或状态机行为的变更
- Helm values 的变更

### 4. 确保集群有足够的资源

```bash
# Check node resources
kubectl top nodes

# Check existing operator pod resource usage
kubectl top pod -l app.kubernetes.io/name=opensaola -n <operator-namespace>
```

### 5. 验证所有中间件实例状态健康

```bash
# Check that all Middleware instances are Available
kubectl get mid -A

# Check that all MiddlewareOperators are Available
kubectl get mo -A

# Check that there are no resources in Updating state
kubectl get mid -A -o jsonpath='{range .items[?(@.status.state!="Available")]}{.metadata.namespace}/{.metadata.name}: {.status.state}{"\n"}{end}'
```

---

## 升级 OpenSaola Operator

### 步骤 1：更新 Helm 仓库

```bash
helm repo update
```

### 步骤 2：查看新版本的配置值

```bash
# View the default values for the new version
helm show values opensaola/opensaola --version <new-version>

# Compare with your current values
helm get values opensaola -n <operator-namespace> > current-values.yaml
```

### 步骤 3：执行升级

```bash
# Upgrade with your existing custom values
helm upgrade opensaola opensaola/opensaola \
  -n <operator-namespace> \
  -f current-values.yaml \
  --version <new-version>

# Or upgrade with specific value overrides
helm upgrade opensaola opensaola/opensaola \
  -n <operator-namespace> \
  --set image.tag=<new-tag> \
  --version <new-version>
```

### 步骤 4：验证 CRD 更新

CRD 通过升级前的 Helm hook（一个 `kubectl apply` Job）进行管理。该 hook 在主 chart 资源升级之前运行。

```bash
# Verify CRDs are at the expected version
kubectl get crds | grep middleware.cn

# Check CRD details
kubectl get crd middlewares.middleware.cn -o jsonpath='{.metadata.resourceVersion}'
```

---

## 升级中间件软件包

中间件软件包（如 Redis、MySQL）的升级独立于 OpenSaola operator。升级通过 Middleware 和 MiddlewareOperator 资源上的 Annotation 触发。

### 步骤 1：上传新版本的软件包

使用 `saola-cli` 上传新版本的软件包：

```bash
saola package upload --path <package-directory>
```

这将在数据命名空间中创建或更新一个包含新软件包内容的 Secret。

### 步骤 2：验证新软件包已发布

```bash
# Check the new MiddlewarePackage
kubectl get mp -l middleware.cn/packageversion=<new-version>

# Verify baselines are created
kubectl get mb -l middleware.cn/packageversion=<new-version>
kubectl get mob -l middleware.cn/packageversion=<new-version>
```

### 步骤 3：触发中间件升级

设置 `middleware.cn/update` 和 `middleware.cn/baseline` Annotation 以触发升级：

```bash
# Upgrade a MiddlewareOperator
kubectl annotate mo <name> -n <namespace> \
  middleware.cn/update=<new-version> \
  middleware.cn/baseline=<new-operator-baseline-name>

# Upgrade a Middleware
kubectl annotate mid <name> -n <namespace> \
  middleware.cn/update=<new-version> \
  middleware.cn/baseline=<new-baseline-name>
```

### 步骤 4：监控升级过程

```bash
# Watch the state transition
kubectl get mid <name> -n <namespace> -w

# Check the Updating condition
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.type == "Updating")'
```

资源将经历以下状态转换：`Available` -> `Updating` -> `Available`（成功时）或 `Unavailable`（失败时）。

---

## 升级后验证

升级 operator 或中间件软件包后，请验证系统是否健康。

### 检查 operator Pod 状态

```bash
# Verify the operator pod is running
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>

# Check for any restarts
kubectl describe pod -l app.kubernetes.io/name=opensaola -n <operator-namespace> | grep -A2 "Restart Count"

# Verify the new image is running
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace> -o jsonpath='{.items[0].spec.containers[0].image}'
```

### 验证 CRD 版本

```bash
# List all OpenSaola CRDs
kubectl get crds | grep middleware.cn

# Check stored versions
kubectl get crd middlewares.middleware.cn -o jsonpath='{.status.storedVersions}'
```

### 检查中间件实例健康状态

```bash
# Verify all Middleware instances are Available
kubectl get mid -A -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,STATE:.status.state,REASON:.status.reason'

# Verify all MiddlewareOperators are Available
kubectl get mo -A -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,STATE:.status.state,REASON:.status.reason'

# Check MiddlewareOperator deployment availability
kubectl get mo -A -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,AVAILABLE:.status.operatorAvailable'

# Verify all packages are Available
kubectl get mp -o custom-columns='NAME:.metadata.name,STATE:.status.state'
```

### 检查 operator 日志中的错误

```bash
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200 | grep -i "error\|fail\|panic"
```

---

## 回滚流程

### 回滚 operator

```bash
# View Helm release history
helm history opensaola -n <operator-namespace>

# Rollback to a previous revision
helm rollback opensaola <revision-number> -n <operator-namespace>

# Verify the rollback
helm list -n <operator-namespace>
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>
```

### CRD 回滚注意事项

CRD **不会**被 `helm rollback` 自动回滚，因为它们是通过升级前的 hook Job 应用的。如果新版本的 CRD 是向后兼容的（即仅添加了新的可选字段），则不需要回滚 CRD。如果新版本的 CRD 引入了破坏性变更：

1. 手动应用旧版本的 CRD 定义：
   ```bash
   kubectl apply -f <old-crd-definitions-directory>/
   ```
2. 验证现有资源在旧版本 CRD schema 下仍然有效

### 回滚中间件软件包

中间件软件包的升级可以通过重新触发升级到先前版本来回滚：

```bash
# Set the update annotation to the previous version
kubectl annotate mid <name> -n <namespace> \
  middleware.cn/update=<previous-version> \
  middleware.cn/baseline=<previous-baseline-name> \
  --overwrite
```

或者，如果升级 Annotation 仍然存在且升级尚未完成，可以移除它们来中止升级：

```bash
kubectl annotate mid <name> -n <namespace> middleware.cn/update- middleware.cn/baseline-
```

---

## CRD 兼容性说明

### CRD 的管理方式

- CRD 通过 pre-install/pre-upgrade Helm hook 进行安装和升级
- 该 hook 使用 `kubectl apply` Job 从 chart 中应用 CRD 清单文件
- 卸载 Helm release 时 CRD **不会被删除**（这是 Helm 对 CRD 的标准行为）

### 向后兼容性规则

| 变更类型 | 是否向后兼容？ | 说明 |
|---------|--------------|------|
| 添加新的可选字段 | 是 | 现有资源不受影响 |
| 添加新的必填字段 | 否 | 现有资源将无法通过验证 |
| 移除字段 | 否 | 引用被移除字段的现有资源将丢失数据 |
| 重命名字段 | 否 | 等同于移除 + 添加，需要迁移 |
| 更改字段类型 | 否 | 现有数据可能变为无效 |
| 添加新的枚举值 | 是 | 现有资源不受影响 |
| 移除枚举值 | 否 | 使用被移除值的现有资源将无法通过验证 |

### 破坏性 CRD 变更的迁移步骤

如果新版本引入了破坏性 CRD 变更，请按照以下步骤操作：

1. 备份所有受影响的资源（参见[升级前检查清单](#1-备份所有自定义资源)）
2. 阅读发布说明中的具体迁移指引
3. 应用新的 CRD
4. 使用 `kubectl edit` 或脚本化的 `kubectl patch` 命令迁移现有资源
5. 升级 operator
6. 验证所有资源是否成功 reconcile

---

## 升级故障排查

有关通用故障排查，请参阅[故障排查指南](troubleshooting.md)。

### Operator 升级问题

| 症状 | 可能原因 | 解决方案 |
|-----|---------|---------|
| 新 Pod 卡在 `CrashLoopBackOff` | 缺少 CRD 或配置不兼容 | 使用 `--previous` 参数检查 Pod 日志 |
| CRD hook Job 失败 | RBAC 或网络问题 | 检查 Job 日志：`kubectl logs job/opensaola-crd-install -n <ns>` |
| 升级后现有资源变为 `Unavailable` | 破坏性 CRD 变更或 reconcile 逻辑变更 | 检查 Condition，查看发布说明 |

### 软件包升级问题

| 症状 | 可能原因 | 解决方案 |
|-----|---------|---------|
| Middleware 卡在 `Updating` 状态 | 新 Baseline 未找到或模板错误 | 检查 `Updating` Condition 消息 |
| 升级 Annotation 被忽略 | 资源因先前尝试已处于 `Updating` 状态 | 先检查并解决当前状态 |
| 新 Baseline 未创建 | Package Secret 缺少 Label 或已损坏 | 使用 `saola-cli` 重新上传 |

### 延伸阅读

- [故障排查指南](troubleshooting.md) -- 常见问题的详细调试方法
- [技术文档](opensaola-technical.md) -- 升级触发机制（第 8 节）、删除流程（第 9 节）
- [软件包文档](opensaola-packaging.md) -- 软件包格式和 Baseline 系统
