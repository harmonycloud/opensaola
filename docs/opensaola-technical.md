# OpenSaola 技术文档

## 目录

- [1. 架构概览](#1-架构概览)
  - [1.1 设计理念](#11-设计理念)
  - [1.2 整体架构图（文字描述）](#12-整体架构图文字描述)
  - [1.3 CRD 资源关系](#13-crd-资源关系)
  - [1.4 与 dataservice-baseline / saola-cli 的交互边界](#14-与-dataservice-baseline--saola-cli-的交互边界)
- [2. CRD 完整字段参考](#2-crd-完整字段参考)
  - [2.1 公共类型（Common Types）](#21-公共类型common-types)
  - [2.2 Middleware](#22-middleware)
  - [2.3 MiddlewareBaseline](#23-middlewarebaseline)
  - [2.4 MiddlewareOperator](#24-middlewareoperator)
  - [2.5 MiddlewareOperatorBaseline](#25-middlewareoperatorbaseline)
  - [2.6 MiddlewarePackage](#26-middlewarepackage)
  - [2.7 MiddlewareAction](#27-middlewareaction)
  - [2.8 MiddlewareActionBaseline](#28-middlewareactionbaseline)
  - [2.9 MiddlewareConfiguration](#29-middlewareconfiguration)
- [3. 状态机与状态流转](#3-状态机与状态流转)
  - [3.1 Phase/State 枚举值](#31-phasestate-枚举值)
  - [3.2 状态流转图](#32-状态流转图)
  - [3.3 Condition 类型](#33-condition-类型)
  - [3.4 状态变更触发条件](#34-状态变更触发条件)
- [4. Labels 与 Annotations 约定](#4-labels-与-annotations-约定)
- [5. 控制器 Reconcile 流程](#5-控制器-reconcile-流程)
  - [5.1 Middleware Controller](#51-middleware-controller)
  - [5.2 MiddlewareBaseline Controller](#52-middlewarebaseline-controller)
  - [5.3 MiddlewareOperator Controller](#53-middlewareoperator-controller)
  - [5.4 MiddlewareOperatorBaseline Controller](#54-middlewareoperatorbaseline-controller)
  - [5.5 MiddlewarePackage Controller](#55-middlewarepackage-controller)
  - [5.6 MiddlewareAction Controller](#56-middlewareaction-controller)
  - [5.7 MiddlewareActionBaseline Controller](#57-middlewareactionbaseline-controller)
  - [5.8 MiddlewareConfiguration Controller](#58-middlewareconfiguration-controller)
- [6. 服务层详解](#6-服务层详解)
  - [6.1 各 service 的职责和核心方法](#61-各-service-的职责和核心方法)
- [7. Watcher / Syncer 机制](#7-watcher--syncer-机制)
  - [7.1 CR Watcher](#71-cr-watcherpkgservicewatchercustomresourcego)
  - [7.2 Synchronizer](#72-synchronizerpkgservicesynchronizersynchronizergo)
- [8. 升级触发机制](#8-升级触发机制)
  - [8.1 触发方式](#81-触发方式)
  - [8.2 升级流程（以 Middleware 为例）](#82-升级流程以-middleware-为例)
  - [8.3 MiddlewareOperator 升级特殊处理](#83-middlewareoperator-升级特殊处理)
- [9. 删除流程与 Finalizer](#9-删除流程与-finalizer)
  - [9.1 Middleware 删除](#91-middleware-删除)
  - [9.2 MiddlewareOperator 删除](#92-middlewareoperator-删除)
  - [9.3 MiddlewarePackage 卸载](#93-middlewarepackage-卸载)
  - [9.4 Secret 删除](#94-secret-删除)
  - [9.5 CR 被误删](#95-cr-被误删)
  - [9.6 Finalizer](#96-finalizer)
- [10. 工具包（pkg/tools）](#10-工具包pkgtools)
  - [10.1 模板渲染（template.go）](#101-模板渲染templatego)
  - [10.2 CUE 处理（cue.go）](#102-cue-处理cuego)
  - [10.3 TAR 操作（tar.go）](#103-tar-操作targo)
  - [10.4 JSON/Map 工具（json.go）](#104-jsonmap-工具jsongo)
  - [10.5 其他工具](#105-其他工具)
- [11. K8s 操作封装（pkg/k8s）](#11-k8s-操作封装pkgk8s)
  - [11.1 Client 工厂（kubeClient/client.go）](#111-client-工厂kubeclientclientgo)
  - [11.2 CRD 资源 CRUD](#112-crd-资源-crud)
  - [11.3 原生资源操作](#113-原生资源操作)
  - [11.4 Informer（informer.go）](#114-informerinformergo)
  - [11.5 Patch 工具（patch.go）](#115-patch-工具patchgo)
  - [11.6 Pod Exec（k8s.go）](#116-pod-execk8sgo)
- [12. 关键设计决策](#12-关键设计决策)

---

## 1. 架构概览

### 1.1 设计理念

OpenSaola 是一个基于 Kubernetes Operator 模式构建的中间件全生命周期管理平台。其核心设计理念包括：

- **声明式管理**：通过 CRD 声明中间件的期望状态，由 Operator 驱动实际状态向期望状态收敛
- **包驱动发布**：中间件能力以 Package（包）形式打包分发，包内包含 Baseline（基线模板）、Configuration（配置模板）、Action（操作定义）等
- **模板与合并**：支持 Go Template 渲染和结构化深度合并（StructMerge），实现基线与用户自定义参数的叠加
- **CUE 编排**：集成 CUE 语言进行字段映射和资源编排，支持复杂的声明式操作
- **多层抽象**：Baseline（集群级模板） -> Operator/Middleware（命名空间级实例），实现关注点分离

API Group: `middleware.cn`，Version: `v1`

### 1.2 整体架构图（文字描述）

```
                     +-----------------+
                     |   saola-cli     |   （CLI 工具：打包、上传包到 Secret）
                     +--------+--------+
                              |
                              v
                 +------------+------------+
                 |   K8s Secret（Package）  |   （zstd 压缩 + tar 归档）
                 +------------+------------+
                              |
                              v
              +---------------+---------------+
              |  MiddlewarePackage Controller  |  （监听 Secret，解析包，发布 Baseline/Configuration 等）
              +---------------+---------------+
                              |
         +--------------------+--------------------+
         |                    |                    |
         v                    v                    v
+--------+--------+  +-------+--------+  +--------+--------+
| MiddlewareBaseline|  |MiddlewareOperator|  |MiddlewareAction |
|   Controller      |  |Baseline Controller|  |Baseline Controller|
+---------+---------+  +--------+--------+  +--------+--------+
          |                     |                     |
          v                     v                     v
+---------+---------+  +--------+--------+  +--------+--------+
|  Middleware        |  | MiddlewareOperator |  | MiddlewareAction  |
|  Controller        |  |   Controller       |  |   Controller      |
+---------+---------+  +--------+--------+  +--------+--------+
          |                     |                     |
          v                     v                     v
   +------+------+      +------+------+      +-------+-------+
   | CustomResource|      | Deployment   |      | CUE/CMD/HTTP |
   | (CR 实例)     |      | + RBAC       |      | 执行引擎     |
   +------+------+      +------+------+      +-------+-------+
          |                     |
          v                     v
   +------+------+      +------+------+
   | CR Watcher   |      | Deployment   |
   | (Informer)  |      |   Watcher    |
   +------+------+      +------+------+
          |
          v
   +------+------+
   | Synchronizer |  （定时同步 CR 状态到 Middleware.Status.CustomResources）
   +-------------+
```

### 1.3 CRD 资源关系

```
MiddlewarePackage (Cluster)  -- 拥有 -->  MiddlewareBaseline (Cluster)
                             -- 拥有 -->  MiddlewareOperatorBaseline (Cluster)
                             -- 拥有 -->  MiddlewareActionBaseline (Cluster)
                             -- 拥有 -->  MiddlewareConfiguration (Cluster)

MiddlewareOperator (Namespaced) -- 引用 --> MiddlewareOperatorBaseline
                                -- 生成 --> Deployment, ServiceAccount, Role/ClusterRole, RoleBinding/ClusterRoleBinding

Middleware (Namespaced) -- 引用 --> MiddlewareBaseline
                        -- 引用 --> MiddlewareOperatorBaseline（通过 OperatorBaseline 字段间接引用）
                        -- 生成 --> CustomResource（由 Operator 管理的 CR）
                        -- 触发 --> MiddlewareAction（PreActions）

MiddlewareAction (Namespaced) -- 引用 --> MiddlewareActionBaseline
```

### 1.4 与 dataservice-baseline / saola-cli 的交互边界

- **saola-cli**：负责将中间件能力打包为 tar + zstd 格式，上传到 K8s Secret（存储在 `data_namespace`，代码默认值为 `default`，但实际部署环境可通过配置文件 `config.yaml` 的 `data_namespace` 字段覆盖此值；saola-cli 默认使用 `middleware-operator` 命名空间）。Secret 需要包含 `middleware.cn/project: OpenSaola` 标签
- **dataservice-baseline**：提供 40+ 中间件的 CUE/Go 模板、基线定义、Action 定义。这些内容被 saola-cli 打包后上传
- **OpenSaola**：监听带有 `middleware.cn/project: OpenSaola` 标签的 Secret，自动解析包内容并发布 CRD 资源实例（Baseline、Configuration 等）

---

## 2. CRD 完整字段参考

### 2.1 公共类型（Common Types）

#### Permission 权限

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| serviceAccountName | string | 否 | `""` | ServiceAccount 名称 | 同时用作 Role/ClusterRole 和 RoleBinding/ClusterRoleBinding 的名称 |
| rules | []rbacv1.PolicyRule | 否 | nil | RBAC 规则列表 | 标准 K8s PolicyRule 格式 |

#### OperatorBaseline 关联上架模板

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| name | string | 否 | `""` | MiddlewareOperatorBaseline 的名称 | 用于关联 Operator 模板 |
| gvkName | string | 否 | `""` | GVK 名称，用于定位要发布的 CR 类型 | 必须在 OperatorBaseline.Spec.GVKs 中存在匹配项 |

#### Configuration 配置引用

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| name | string | 否 | `""` | MiddlewareConfiguration 的名称 | 必须在包中存在对应的 Configuration |
| values | runtime.RawExtension | 否 | nil | 配置值，JSON 格式 | 支持 Go Template 渲染 |

#### PreAction 前置操作

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| name | string | 否 | `""` | Action 名称，对应 MiddlewareActionBaseline 名称 | 必须是 BaselineType=PreAction 的 ActionBaseline |
| fixed | bool | 否 | false | 是否固定（不可被覆盖） | true 时跳过合并（见下方边界说明） |

> **fixed=true PreAction 合并边界说明**：fixed=true 的 baseline PreAction 不接受用户覆盖，其参数始终使用 baseline 中定义的值。用户在 `Middleware.spec.preActions` 中定义的同名条目将被忽略。用户无法添加 baseline 中未声明的 PreAction。
| exposed | bool | 否 | false | 是否暴露 | - |
| parameters | runtime.RawExtension | 否 | nil | 前置操作参数 | JSON 格式 |

#### Globe 全局配置

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| repository | string | 否 | `""` | 镜像仓库地址 | - |

#### State 状态枚举

| 值 | 说明 |
|----|------|
| `Available` | 可用 |
| `Unavailable` | 不可用 |
| `Updating` | 更新中 |

#### Phase 阶段枚举

| 值 | 说明 |
|----|------|
| `""` (空) | 未知/初始 |
| `Checking` | 校验中 |
| `Checked` | 校验完成 |
| `Running` | 运行中 |
| `Failed` | 失败 |
| `UpdatingCustomResources` | 更新 CR 中 |
| `BuildingRBAC` | 构建 RBAC 中 |
| `BuildingDeployment` | 构建 Deployment 中 |
| `Finished` | 完成 |
| `MappingFields` | 映射字段中 |
| `Executing` | 执行中 |

#### PermissionScope 权限作用域枚举

| 值 | 说明 |
|----|------|
| `Unknown` | 未知 |
| `Cluster` | 集群级（生成 ClusterRole + ClusterRoleBinding） |
| `Namespace` | 命名空间级（生成 Role + RoleBinding） |

#### ConfigurationType 配置类型枚举

| 值 | 说明 |
|----|------|
| `""` (空) | 未知 |
| `configmap` | ConfigMap |
| `serviceAccount` | ServiceAccount |
| `role` | Role |
| `roleBinding` | RoleBinding |
| `clusterRole` | ClusterRole |
| `clusterRoleBinding` | ClusterRoleBinding |
| `customResource` | 自定义资源 |
| `customResourceBaseline` | 自定义资源基线 |

#### BaselineType 基线类型枚举

| 值 | 说明 |
|----|------|
| `PreAction` | 前置操作（在资源发布前执行） |
| `NormalAction` | 普通操作（由用户主动触发） |

> **关于 OpsAction**：`OpsAction` 并非源码中显式定义的 `BaselineType` 常量（源码仅定义了 `WorkflowPreAction = "PreAction"` 和 `WorkflowNormalAction = "NormalAction"`），但实际 YAML 文件中广泛使用了此值（如 `restart.yaml` 中 `baselineType: "OpsAction"`）。控制器通过 `BaselineType != PreAction` 的排除逻辑判断是否执行，因此 `OpsAction` 行为等同于 `NormalAction`，可以正常工作。

#### Condition Type 常量

| 常量 | 说明 |
|------|------|
| `Checked` | 校验条件 |
| `BuildPreResource` | 构建前置资源 |
| `BuildExtraResource` | 构建额外资源 |
| `ApplyRBAC` | 应用 RBAC |
| `ApplyOperator` | 应用 Operator（Deployment） |
| `ApplyCluster` | 应用 CR |
| `MapCueFields` | 映射 CUE 字段 |
| `ExecuteAction` | 执行 Action |
| `ExecuteCue` | 执行 CUE |
| `ExecuteCmd` | 执行命令 |
| `ExecuteHttp` | 执行 HTTP 请求 |
| `Running` | 运行中 |
| `TemplateParseWithBaseline` | 模板解析与基线合并 |
| `Updating` | 更新中 |

#### Condition Reason 常量参考表

> 源码位置：`OpenSaola/api/v1/common.go:120-147`

| 常量名 | 值 | 对应 CondType | 说明 |
|--------|------|--------------|------|
| `CondReasonUnknown` | `"Unknown"` | 通用 | 未知状态 |
| `CondReasonIniting` | `"Initing"` | 通用 | Condition 初始化时的默认 Reason |
| `CondReasonCheckedFailed` | `"CheckedFailed"` | `Checked` | 校验失败 |
| `CondReasonCheckedSuccess` | `"CheckedSuccess"` | `Checked` | 校验成功 |
| `CondReasonBuildExtraResourceSuccess` | `"BuildExtraResourceSuccess"` | `BuildExtraResource` | 构建额外资源成功 |
| `CondReasonBuildExtraResourceFailed` | `"BuildExtraResourceFailed"` | `BuildExtraResource` | 构建额外资源失败 |
| `CondReasonApplyRBACSuccess` | `"ApplyRBACSuccess"` | `ApplyRBAC` | 应用 RBAC 成功 |
| `CondReasonApplyRBACFailed` | `"ApplyRBACFailed"` | `ApplyRBAC` | 应用 RBAC 失败 |
| `CondReasonApplyOperatorSuccess` | `"ApplyOperatorSuccess"` | `ApplyOperator` | 应用 Operator（Deployment）成功 |
| `CondReasonApplyOperatorFailed` | `"ApplyOperatorFailed"` | `ApplyOperator` | 应用 Operator（Deployment）失败 |
| `CondReasonApplyClusterSuccess` | `"ApplyClusterSuccess"` | `ApplyCluster` | 应用 CR 成功 |
| `CondReasonApplyClusterFailed` | `"ApplyClusterFailed"` | `ApplyCluster` | 应用 CR 失败 |
| `CondReasonMapCueFieldsSuccess` | `"MapCueFieldsSuccess"` | `MapCueFields` | 映射 CUE 字段成功 |
| `CondReasonMapCueFieldsFailed` | `"MapCueFieldsFailed"` | `MapCueFields` | 映射 CUE 字段失败 |
| `CondReasonExecuteActionSuccess` | `"ExecuteActionSuccess"` | `ExecuteAction` | 执行 Action 成功 |
| `CondReasonExecuteActionFailed` | `"ExecuteActionFailed"` | `ExecuteAction` | 执行 Action 失败 |
| `CondReasonExecuteCueSuccess` | `"ExecuteCueSuccess"` | `ExecuteCue` | 执行 CUE 成功 |
| `CondReasonExecuteCueFailed` | `"ExecuteCueFailed"` | `ExecuteCue` | 执行 CUE 失败 |
| `CondReasonExecuteCmdSuccess` | `"ExecuteCmdSuccess"` | `ExecuteCmd` | 执行命令成功 |
| `CondReasonExecuteCmdFailed` | `"ExecuteCmdFailed"` | `ExecuteCmd` | 执行命令失败 |
| `CondReasonRunningSuccess` | `"RunningSuccess"` | `Running` | 运行成功 |
| `CondReasonRunningFailed` | `"RunningFailed"` | `Running` | 运行失败 |
| `CondReasonUpdatingSuccess` | `"UpdatingSuccess"` | `Updating` | 升级成功 |
| `CondReasonUpdatingFailed` | `"UpdatingFailed"` | `Updating` | 升级失败 |
| `CondReasonTemplateParseWithBaselineSuccess` | `"TemplateParseWithBaselineSuccess"` | `TemplateParseWithBaseline` | 模板解析与基线合并成功 |
| `CondReasonTemplateParseWithBaselineFaild` | `"TemplateParseWithBaselineFaild"` | `TemplateParseWithBaseline` | 模板解析与基线合并失败（**注意**：源码中拼写为 `Faild`，应为 `Failed`，属于源码拼写错误，已存在于线上代码中） |

---

### 2.2 Middleware

**作用域**：Namespaced  
**简称**：`mid`  
**打印列**：Type, Package, Baseline, Status, Age

#### 2.2.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| operatorBaseline | OperatorBaseline | 否 | 零值 | 关联的 Operator 基线 | 如果为空则取 MiddlewareBaseline 中的值 |
| baseline | string | 否 | `""` | 关联的 MiddlewareBaseline 名称 | 必须在包中存在 |
| necessary | runtime.RawExtension | 否 | nil | 必填参数（如 image 等） | JSON 格式；与 Baseline 中的 necessary 对比，缺少则报错（repository 除外） |
| preActions | []PreAction | 否 | nil | 前置操作列表 | 与 Baseline 的 preActions 合并，fixed=true 的不合并 |
| parameters | runtime.RawExtension | 否 | nil | 自定义参数 | kubebuilder:pruning:PreserveUnknownFields；与 Baseline 深度合并 |
| configurations | []Configuration | 否 | nil | 配置列表 | 与 Baseline 的 configurations 数组合并 |

**接口方法**：
- `GetConfigurations()` - 返回 Spec.Configurations
- `GetUnified()` - 返回 &Spec.Necessary
- `GetPreActions()` - 返回 Spec.PreActions
- `GetMiddlewareName()` - 返回 Name

#### 2.2.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | 已观察到的 Generation | 用于判断是否需要更新 |
| conditions | []metav1.Condition | 否 | nil | 条件列表 | patchMergeKey=type，patchStrategy=merge |
| customResources | CustomResources | 否 | 零值 | 关联 CR 的状态快照 | 由 Synchronizer 定时同步 |
| state | State | 否 | `""` | 整体状态 | 任何 Condition 为 False 则为 Unavailable |
| reason | string | 否 | `""` | 状态原因 | 取第一个 False Condition 的 Message |

#### CustomResources 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| creationTimestamp | metav1.Time | CR 创建时间 |
| phase | Phase | CR 的 Phase/State |
| resources | v1.ResourceRequirements | 资源需求 |
| replicas | int | 副本数 |
| reason | string | 原因 |
| type | string | 类型 |
| include | Include | 关联资源列表 |
| disaster | *Disaster | 灾备信息（指针，可为 nil） |

#### Include 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| pods | []IncludeModel | 关联的 Pod 列表 |
| pvcs | []IncludeModel | 关联的 PVC 列表 |
| services | []IncludeModel | 关联的 Service 列表 |
| statefulsets | []IncludeModel | 关联的 StatefulSet 列表 |
| deployments | []IncludeModel | 关联的 Deployment 列表 |
| daemonsets | []IncludeModel | 关联的 DaemonSet 列表 |

#### IncludeModel 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| name | string | 资源名称 |
| type | string | 类型（从 conditions 中获取） |
| source | string | 来源标签 |
| sourceName | string | 来源名称标签 |

#### Disaster 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| gossip | *Gossip | Gossip 灾备信息 |
| data | *Data | 数据同步信息 |

#### Gossip 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| advertiseAddress | string | 广播地址 |
| advertisePort | int64 | 广播端口 |
| phase | string | Gossip 阶段 |
| clusterRole | string | 集群角色 |
| clusterPhase | string | 集群阶段 |
| role | string | 角色 |
| gossipPhase | string | Gossip 阶段 |

#### Data 子结构

| 字段名 | 类型 | JSON 标签 | 说明 |
|--------|------|-----------|------|
| phase | string | `phase` | 阶段 |
| address | string | `targetAddress` | 目标地址 |
| oppositeAddress | string | `oppositeAddress` | 对端地址 |
| oppositeClusterId | string | `oppositeClusterId` | 对端集群 ID |
| oppositeClusterName | string | `oppositeClusterName` | 对端集群名称 |
| oppositeClusterNamespace | string | `oppositeClusterNamespace` | 对端集群命名空间 |

---

### 2.3 MiddlewareBaseline

**作用域**：Cluster  
**简称**：`mb`  
**打印列**：Type, Package, Status, Age

#### 2.3.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| operatorBaseline | OperatorBaseline | 否 | 零值 | 关联的 Operator 基线 | - |
| necessary | runtime.RawExtension | 否 | nil | 必填参数定义 | 定义 Middleware 需要提供的必填参数 |
| preActions | []PreAction | 否 | nil | 前置操作定义 | - |
| parameters | runtime.RawExtension | 否 | nil | 默认参数模板 | kubebuilder:pruning:PreserveUnknownFields |
| configurations | []Configuration | 否 | nil | 默认配置列表 | - |

#### 2.3.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | 已观察到的 Generation | - |
| conditions | []metav1.Condition | 否 | nil | 条件列表 | - |
| state | State | 否 | `""` | 状态 | - |

**接口方法**：
- `GetConfigurations()` - 返回 Spec.Configurations
- `GetUnified()` - 返回 nil（Baseline 不提供 Unified/Necessary 值）

---

### 2.4 MiddlewareOperator

**作用域**：Namespaced  
**简称**：`mo`  
**打印列**：Type, Package, Baseline, Status, Age

#### 2.4.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| baseline | string | 是 | `""` | 关联的 MiddlewareOperatorBaseline 名称 | 校验时若为空则标记 Checked=False |
| globe | *runtime.RawExtension | 否 | nil | 全局变量（指针） | 用于模板渲染，相当于 Middleware 的 necessary |
| preActions | []PreAction | 否 | nil | 前置操作列表 | - |
| permissionScope | PermissionScope | 否 | `""` | 权限作用域 | 决定生成 Role 还是 ClusterRole |
| permissions | []Permission | 否 | nil | 权限列表 | 每个 Permission 生成 SA + Role/CR + RB/CRB |
| deployment | *runtime.RawExtension | 否 | nil | Deployment 定义（指针） | JSON 格式的 appsv1.Deployment；为 nil 或容器为空则跳过 |
| configurations | []Configuration | 否 | nil | 配置列表 | - |

#### 2.4.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | 已观察到的 Generation | - |
| conditions | []metav1.Condition | 否 | nil | 条件列表 | - |
| state | State | 否 | `""` | 状态 | - |
| operators | map[string]appsv1.DeploymentStatus | 否 | nil | 关联 Deployment 状态 | key 为 Deployment 名称 |
| operatorAvailable | string | 否 | `""` | 可用性（格式 "available/replicas"） | 如 "1/1" |
| reason | string | 否 | `""` | 原因 | - |

**接口方法**：
- `GetConfigurations()` - 返回 Spec.Configurations
- `GetUnified()` - 返回 Spec.Globe
- `GetPreActions()` - 返回 Spec.PreActions
- `GetMiddlewareName()` - 返回空字符串

---

### 2.5 MiddlewareOperatorBaseline

**作用域**：Cluster  
**简称**：`mob`  
**打印列**：Type, Package, Status, Age

#### 2.5.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| gvks | []GVK | 否 | nil | 支持的 GVK 列表 | 校验时不能为空；每个 GVK 的 name/group/version/kind 不能为空 |
| globe | *runtime.RawExtension | 否 | nil | 全局变量模板 | - |
| preActions | []PreAction | 否 | nil | 前置操作定义 | - |
| permissionScope | PermissionScope | 否 | `""` | 权限作用域 | - |
| permissions | []Permission | 否 | nil | 权限定义 | - |
| deployment | *runtime.RawExtension | 否 | nil | Deployment 模板 | - |
| configurations | []Configuration | 否 | nil | 配置列表 | - |

#### GVK 子结构

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| name | string | 是 | `""` | GVK 名称标识 | 校验时不能为空 |
| group | string | 是 | `""` | API Group | 校验时不能为空 |
| version | string | 是 | `""` | API Version | 校验时不能为空 |
| kind | string | 是 | `""` | Kind | 校验时不能为空 |

#### 2.5.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | 已观察到的 Generation | - |
| conditions | []metav1.Condition | 否 | nil | 条件列表 | - |
| state | State | 否 | `""` | 状态 | GVK 校验失败则 Unavailable |

**接口方法**：
- `GetConfigurations()` - 返回 Spec.Configurations
- `GetUnified()` - 返回 Spec.Globe

---

### 2.6 MiddlewarePackage

**作用域**：Cluster  
**简称**：`mp`  
**打印列**：Type, Age

#### 2.6.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| name | string | 否 | `""` | 包名称 | 来自 metadata.yaml |
| catalog | Catalog | 否 | 零值 | 包目录 | - |
| version | string | 否 | `""` | 版本号 | - |
| owner | string | 否 | `""` | 拥有者 | - |
| type | string | 否 | `""` | 类型 | - |
| description | string | 否 | `""` | 描述 | - |

#### Catalog 子结构

| 字段名 | 类型 | JSON 标签 | 说明 |
|--------|------|-----------|------|
| crds | []string | `crds` | CRD 文件名列表 |
| baselines | []string | `definitions` | 基线文件名列表（注意 JSON 标签为 definitions） |
| configurations | []string | `configurations` | 配置文件名列表 |
| actions | []string | `actions` | Action 文件名列表 |

#### 2.6.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | - | - |
| conditions | []metav1.Condition | 否 | nil | - | - |
| state | State | 否 | `""` | - | - |

---

### 2.7 MiddlewareAction

**作用域**：Namespaced  
**简称**：`ma`  
**打印列**：Status, Age, Reason

#### 2.7.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| baseline | string | 是 | - | 关联的 MiddlewareActionBaseline 名称 | JSON 标签无 omitempty |
| necessary | runtime.RawExtension | 否 | nil | 必填参数 | - |
| middlewareName | string | 是 | - | 关联的 Middleware 名称 | JSON 标签无 omitempty |

#### 2.7.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | - | - |
| conditions | []metav1.Condition | 否 | nil | 每个 Step 一个 Condition（类型为 "STEP-{stepName}"） | - |
| state | State | 否 | `""` | - | 如果 State 非空则跳过 Reconcile（一次性执行） |
| reason | metav1.StatusReason | 否 | `""` | 失败原因 | 格式为 "conditionType,message" |

**接口方法**：
- `GetConfigurations()` - 返回 nil
- `GetUnified()` - 返回 &Spec.Necessary
- `GetPreActions()` - 返回空切片
- `GetMiddlewareName()` - 返回 Spec.MiddlewareName

---

### 2.8 MiddlewareActionBaseline

**作用域**：Cluster  
**简称**：`mab`  
**打印列**：Type, Package, Status, Age

#### 2.8.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| baselineType | BaselineType | 是 | - | 基线类型 | PreAction 或 NormalAction |
| actionType | string | 是 | - | 操作类型 | - |
| supportedBaselines | []string | 否 | nil | 支持的基线列表 | - |
| necessary | runtime.RawExtension | 否 | nil | 必填参数定义 | - |
| steps | []Step | 是 | - | 执行步骤列表 | 校验时不能为空；每个 step.name 不能为空 |

#### Step 子结构

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| name | string | 是 | - | 步骤名称 | 校验时不能为空 |
| type | string | 否 | `""` | 步骤类型 | `KubectlGet` / `KubectlExec` / `KubectlEdit` |
| output | Output | 否 | 零值 | 输出配置 | - |
| cue | string | 否 | `""` | CUE 表达式 | 编译校验 |
| cmd | CMD | 否 | 零值 | 命令配置 | - |
| http | Http | 否 | 零值 | HTTP 请求配置 | - |

#### Output 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| expose | bool | 是否暴露输出到 Context（供后续步骤引用） |
| type | string | 输出类型：`json` / `yaml` / `string` |

#### CMD 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| command | []string | 命令参数列表（通过 `sh -c` 执行，join 为单行命令） |

#### Http 子结构

| 字段名 | 类型 | 说明 |
|--------|------|------|
| method | string | HTTP 方法 |
| url | string | 请求 URL |
| header | map[string]string | 请求头 |
| body | string | 请求体 |

#### 2.8.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | - | - |
| conditions | []metav1.Condition | 否 | nil | - | - |
| state | State | 否 | `""` | - | Steps 校验失败则 Unavailable |

---

### 2.9 MiddlewareConfiguration

**作用域**：Cluster  
**简称**：`mcf`  
**打印列**：Type, Package, Status, Age

#### 2.9.1 Spec 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| template | string | 是 | - | Go Template 格式的 YAML 模板 | kubebuilder:pruning:PreserveUnknownFields；渲染后 yaml.Unmarshal 为 unstructured.Unstructured |

#### 2.9.2 Status 字段

| 字段名 | 类型 | 必填 | 默认值 | 说明 | 边界条件 |
|--------|------|------|--------|------|----------|
| observedGeneration | int64 | 否 | 0 | - | - |
| conditions | []metav1.Condition | 否 | nil | - | - |
| gvks | []metav1.GroupVersionKind | 否 | nil | 使用者 GVK 列表 | JSON 标签为 `gvks` |
| state | State | 否 | `""` | - | - |

---

## 3. 状态机与状态流转

### 3.1 Phase/State 枚举值

**State**（用于所有 CRD 的 Status.State）：
- `Available` - 所有 Condition 均为 True
- `Unavailable` - 不可用（触发条件因 CRD 类型而异，见下文）
- `Updating` - 升级流程中

**Unavailable 判定规则差异**：
- **Middleware / MiddlewareOperator**：仅当任何 Condition 为 **False** 时标记为 Unavailable（Unknown 不触发）
- **MiddlewareAction**：当任何 Condition 为 **False 或 Unknown** 时均标记为 Unavailable

**Phase**（用于 CustomResources.Phase，从底层 CR 的 status 同步）：
- `""`, `Checking`, `Checked`, `Running`, `Failed`, `UpdatingCustomResources`, `BuildingRBAC`, `BuildingDeployment`, `Finished`, `MappingFields`, `Executing`

### 3.2 状态流转图

#### Middleware/MiddlewareOperator 状态流转

```
创建/更新
    |
    v
[Check] -- 失败 --> Unavailable (Checked=False)
    |
    | 成功
    v
[是否有 LabelUpdate 注解?]
    |
    |-- 是 --> [ReplacePackage] -- 失败 --> Unavailable (Updating=False)
    |              |
    |              | 成功
    |              v
    |          State=Updating --> State=Available (Updating=True)
    |
    |-- 否 --> [判断 Generation vs ObservedGeneration]
                   |
                   |-- generation == observedGeneration && 未初始化完成 || observedGeneration == 0
                   |       --> [HandleResource(Publish)]
                   |
                   |-- generation > observedGeneration || state == Updating
                   |       --> [HandleResource(Update)]
                   |
                   |-- 其他（已初始化 && generation == observedGeneration && state != Updating）
                   |       --> 仅同步状态（defer 中根据 Conditions 计算 State）
                   |       注：这是稳态运行的主要路径
                   
HandleResource 流程:
    Checked=True?
        |-- 否 --> 跳过
        |-- 是 -->
            [TemplateParseWithBaseline] -- 失败 --> TemplateParseWithBaseline=False
                |
                v
            [HandlePreActions] -- 失败 --> 返回错误
                |
                v
            [HandleExtraResource] -- 失败 --> BuildExtraResource=False
                |
                v
            [BuildCustomResource / HandleRBAC + BuildDeployment] -- 失败 --> 对应 Condition=False
                |
                v
            所有 Condition=True --> State=Available
```

#### MiddlewareAction 状态流转

```
创建
    |
    v
[State 非空?] -- 是 --> 跳过（一次性执行完毕）
    |
    | 否
    v
[Check] -- 失败 --> Unavailable
    |
    | 成功
    v
[获取 ActionBaseline]
    |
    v
[BaselineType == PreAction?]
    |-- 是 --> 跳过执行（PreAction 在 Middleware/MO 的 HandleResource 中执行）
    |-- 否 --> [Execute] 逐步执行 Steps
                  |
                  | 每步生成 STEP-{name} Condition
                  v
              所有步骤完成 --> State=Available
              任何步骤失败 --> State=Unavailable, Reason="type,message"
```

### 3.3 Condition 类型

每个 Condition 包含以下字段：
- `Type`: 条件类型（见 2.1 中的常量表）
- `Status`: `True` / `False` / `Unknown`
- `Reason`: 对应的 CondReason 常量
- `Message`: 详细信息（成功时为 "成功"，失败时为错误信息）
- `LastTransitionTime`: 最后转换时间
- `ObservedGeneration`: 观察到的 Generation

Condition 初始化时：Status=Unknown, Reason=Initing, Message="初始化中"

### 3.4 状态变更触发条件

| 触发事件 | 影响的 CRD | 行为 |
|----------|-----------|------|
| Spec 变更（Generation 增加） | 所有 CRD | 重新 Reconcile |
| Status 变更 | Middleware | **被忽略**（Predicate 过滤） |
| 添加 `middleware.cn/update` 注解 | Middleware, MiddlewareOperator | 触发升级流程 |
| 添加 `middleware.cn/install` 注解到 Secret | MiddlewarePackage | 触发包安装 |
| 添加 `middleware.cn/uninstall` 注解到 Secret | MiddlewarePackage | 触发包卸载 |
| Deployment 变更 | MiddlewareOperator（Owns Deployment） | 同步 Deployment 状态并比较差异 |
| Secret 创建/更新/删除（带 project 标签） | MiddlewarePackage（Watches Secret） | 创建/更新/删除 MiddlewarePackage |
| CR 对象删除 | Middleware（通过 Watcher） | 自动重建 CR |

---

## 4. Labels 与 Annotations 约定

### Labels

| Key | 用途 | 设置时机 | 示例值 |
|-----|------|----------|--------|
| `middleware.cn/component` | 中间件组件类型 | Package 发布时设置 | `redis`, `mysql` |
| `middleware.cn/packageversion` | 包版本号 | Package 发布时设置 | `1.0.0` |
| `middleware.cn/packagename` | 包名称（Secret 名称） | Package 发布时设置 | `redis-7.0.0-abc123` |
| `middleware.cn/project` | 所属项目 | Secret 创建时设置 | `OpenSaola` |
| `middleware.cn/app` | 应用名称 | Configuration 发布时设置 | `my-redis` |
| `middleware.cn/enabled` | 是否启用 | 安装/卸载时设置 | `true` / `false` |
| `middleware.cn/source` | 来源标识 | 资源创建时 | - |
| `middleware.cn/sourcename` | 来源名称 | 资源创建时 | - |
| `middleware.cn/definition` | 标识 CR 所使用的 baseline 名称（创建来源） | saola-cli 创建资源时设置 | `redis-master-slave` |

### Annotations

| Key | 用途 | 设置时机 | 示例值 |
|-----|------|----------|--------|
| `middleware.cn/update` | 升级目标版本 | 用户触发升级时设置 | `2.0.0` |
| `middleware.cn/baseline` | 升级目标基线名称 | 用户触发升级时设置 | `redis-baseline-v2` |
| `middleware.cn/install` | 安装标记 | 用户触发安装时设置 | 存在即触发 |
| `middleware.cn/uninstall` | 卸载标记 | 用户触发卸载时设置 | 存在即触发 |
| `middleware.cn/configurations` | 关联的 Configuration 名称 | Configuration 发布时设置 | `redis-configmap` |
| `middleware.cn/disasterSyncer` | 灾备同步器 GVK/Name | 用户配置 | `group/version/kind/name` |
| `middleware.cn/dataSyncer` | 数据同步器 GVK/Name | 用户配置 | `group/version/kind/name` |
| `middleware.cn/oppositeClusterId` | 对端集群 ID | 用户配置 | - |

---

## 5. 控制器 Reconcile 流程

### 5.1 Middleware Controller

**Watch 资源**：`v1.Middleware`（自身）  
**Predicate 过滤器**：忽略仅 Status 字段变更的 Update 事件  
**全局变量**：`MiddlewareInitializationCompleted` - 标记是否完成初始化

> **MiddlewareInitializationCompleted 边界说明**：
> 1. 作用：确保 Operator 启动时对已存在的 Middleware 做一次全量 Publish（即使 generation == observedGeneration，也会执行 HandleResource(Publish)）
> 2. 仅第一个成功执行的 Reconcile 会将此标志设为 true，后续 Reconcile 进入正常的 generation 比较逻辑
> 3. 新创建的 Middleware（observedGeneration == 0）不受此标志影响，因为 `observedGeneration == 0` 条件独立于此标志，始终会触发 Publish

**Reconcile 主逻辑**：

1. **获取 Middleware**：通过 `k8s.GetMiddleware` 获取
   - 如果 NotFound：从 `MiddlewareCache` 获取缓存副本，调用 `HandleResource(Delete)` 删除关联资源，清除缓存
   - 如果其他错误：返回错误

2. **defer 状态更新**：
   - 遍历所有 Conditions，如果任何 Condition 为 False，State = Unavailable，Reason = 该 Condition 的 Message
   - 否则 State = Available，Reason = ""
   - 调用 `k8s.UpdateMiddlewareStatus` 更新状态

3. **Check 校验**：调用 `middleware.Check`
   - 设置 Checked Condition 为 True

4. **ReplacePackage 升级检查**：调用 `middleware.ReplacePackage`
   - 检查 `middleware.cn/update` 注解是否存在

5. **Generation 判断**：
   - `generation == observedGeneration && !初始化完成` 或 `observedGeneration == 0`：调用 `HandleResource(Publish)`
   - `generation > observedGeneration` 或 `state == Updating`：调用 `HandleResource(Update)`

**HandleResource 调用链**：
```
HandleResource(Publish/Update/Delete)
    -> TemplateParseWithBaseline  （获取 Baseline -> 深度合并参数/配置/标签/注解/PreActions -> 模板渲染）
    -> HandlePreActions           （遍历 PreActions，执行 PreAction 类型的 ActionBaseline）
    -> handleExtraResource        （获取并渲染 MiddlewareConfigurations -> 逐个 Handle）
    -> buildCustomResource        （通过 GVK 定位 CR 类型 -> 创建/更新/删除 CR -> 启动 Watcher + Synchronizer）
```

### 5.2 MiddlewareBaseline Controller

**Watch 资源**：`v1.MiddlewareBaseline`  
**Predicate**：`GenerationChangedPredicate`（仅 Generation 变化时触发）

**Reconcile 主逻辑**：
1. 获取 MiddlewareBaseline
2. 调用 `middlewarebaseline.Check` - 设置 Checked=True，State=Available，更新状态

### 5.3 MiddlewareOperator Controller

**Watch 资源**：`v1.MiddlewareOperator`（自身），`appsv1.Deployment`（Owns）  
**全局变量**：`MiddlewareOperatorInitializationCompleted`

> **注意**：与 Middleware Controller 不同，MiddlewareOperator 的 `SetupWithManager` **未配置** Status 变更过滤的 Predicate，因此 Status 变更也会触发 Reconcile。Middleware Controller 使用了自定义 `predicate.Funcs` 过滤掉仅 Status 变化的 Update 事件，而 MiddlewareOperator Controller 直接使用默认行为。

**Reconcile 主逻辑**（分两阶段）：

**阶段 1 - handleMiddlewareOperator**：
1. 获取 MiddlewareOperator
   - NotFound：从缓存获取 -> HandleResource(Delete) -> 清缓存
2. defer 状态更新（与 Middleware 相同逻辑）
3. Check 校验：验证 baseline 不为空
4. ReplacePackage 升级检查
5. Generation 判断 -> HandleResource(Publish/Update)

**阶段 2 - handleDeployment**：
1. 获取 MiddlewareOperator，检查是否有 `LabelUpdate` 注解（正在更新则跳过）
2. 获取 Deployment
   - 成功：调用 `CompareDeployment` 比较实际与期望的差异，不一致则恢复
   - 成功：通过 OwnerReference 关联到 MO，同步 Deployment Status 到 MO.Status
   - NotFound 但 ApplyOperator=True：重新 HandleResource(Publish) 重建

**HandleResource 调用链**：
```
HandleResource(Publish/Update/Delete)
    -> TemplateParseWithBaseline  （获取 OperatorBaseline -> 深度合并 Spec -> 模板渲染整个 Spec）
    -> HandlePreActions           （执行前置操作）
    -> handleExtraResource        （发布额外资源 - 与 Middleware 相同）
    -> handleRBAC                 （生成 SA + Role/ClusterRole + RB/CRB）
    -> buildDeployment            （创建/更新/删除 Deployment，设置 ControllerReference）
```

### 5.4 MiddlewareOperatorBaseline Controller

**Watch 资源**：`v1.MiddlewareOperatorBaseline`  
**Predicate**：`GenerationChangedPredicate`

**Reconcile 主逻辑**：
1. 获取 MiddlewareOperatorBaseline
2. 调用 `middlewareoperatorbaseline.Check`
   - 校验 GVKs 不为空
   - 校验每个 GVK 的 name/group/version/kind 不为空
   - 校验失败 -> State=Unavailable，Checked=False

### 5.5 MiddlewarePackage Controller

**Watch 资源**：`v1.MiddlewarePackage`（自身），`corev1.Secret`（Watches）  
**Secret 过滤**：仅处理 `middleware.cn/project: OpenSaola` 标签的 Secret

**Reconcile 主逻辑**：

**HandlePackage**：
1. 获取 MiddlewarePackage
2. 调用 `middlewarepackage.Check` - 设置 Checked=True

**HandleSecret**（由 Secret Watch 触发）：
1. 获取 Secret
   - **存在**：
     - 解析包 -> 创建/更新 MiddlewarePackage
     - 有 `install` 注解：禁用同组件的其他已启用包 -> HandleResource(Publish)（发布 Baseline/Config/Action） -> 设置 enabled=true
     - 有 `uninstall` 注解：HandleResource(Delete) -> 设置 enabled=false
   - **不存在**（删除事件）：
     - 删除对应的 MiddlewarePackage

**HandleResource(Publish) 调用链**：
```
HandleResource(Publish)
    -> 获取包中所有 MiddlewareBaseline -> 逐个 Deploy（设置 labels + OwnerReference -> Create）
    -> 获取包中所有 MiddlewareOperatorBaseline -> 逐个 Deploy
    -> 获取包中所有 MiddlewareActionBaseline -> 逐个 Deploy
    -> 获取包中所有 MiddlewareConfiguration -> 逐个 Deploy
    （每一步失败都会回滚已发布的同类资源）
```

### 5.6 MiddlewareAction Controller

**Watch 资源**：`v1.MiddlewareAction`  
**Predicate**：`GenerationChangedPredicate`

**Reconcile 主逻辑**：
1. 获取 MiddlewareAction
2. **State 非空检查**：如果 State 不为空，直接返回（一次性执行语义）
3. defer 状态更新（与 Middleware 类似，但 Unknown 也视为 Unavailable）
4. Check 校验
5. 获取 ActionBaseline
6. 如果 BaselineType != PreAction：调用 `Execute` 执行所有 Steps
   - 每个 Step 根据类型执行 CUE / CMD / HTTP
   - 每个 Step 生成独立的 Condition（类型 "STEP-{name}"）

### 5.7 MiddlewareActionBaseline Controller

**Watch 资源**：`v1.MiddlewareActionBaseline`  
**Predicate**：`GenerationChangedPredicate`

**Reconcile 主逻辑**：
1. 获取 MiddlewareActionBaseline
2. 调用 `middlewareactionbaseline.Check`
   - 校验 Steps 不为空
   - 校验每个 Step 的 name 不为空
   - 如果有 CUE 步骤可选编译校验

### 5.8 MiddlewareConfiguration Controller

**Watch 资源**：`v1.MiddlewareConfiguration`  
**Predicate**：`GenerationChangedPredicate`

**Reconcile 主逻辑**：
1. 获取 MiddlewareConfiguration
2. 调用 `middlewareconfiguration.Check` - 设置 Checked=True，State=Available

---

## 6. 服务层详解

### 6.1 各 service 的职责和核心方法

#### middleware（pkg/service/middleware/middleware.go）

| 方法 | 职责 |
|------|------|
| `Check` | 校验 Middleware，设置 Checked Condition |
| `ReplacePackage` | 处理升级流程：获取新包 -> 获取新 Baseline -> 更新 Spec/Labels -> 等待包就绪 |
| `TemplateParseWithBaseline` | 获取 Baseline -> 深度合并参数/配置/标签/注解/PreActions -> 模板渲染 Parameters、Configurations、ObjectMeta |
| `HandleResource` | 总控：模板解析 -> PreActions -> 额外资源 -> CR |
| `handleExtraResource` | 获取并渲染 MiddlewareConfigurations -> 逐个 Handle（正序发布，倒序删除） |
| `buildCustomResource` | 通过 GVK 获取 CR 类型 -> 创建/更新 CR -> 启动 Watcher + Synchronizer |

**NecessaryIgnore**：`["repository"]` - 校验必填参数时忽略的键列表

#### middlewarebaseline（pkg/service/middlewarebaseline/）

| 方法 | 职责 |
|------|------|
| `Check` | 校验并更新状态 |
| `Get` | 从集群获取，或从包中获取并缓存（`sync.Map`） |
| `Deploy` | 设置 labels + OwnerReference -> 创建资源 |

#### middlewareoperator（pkg/service/middlewareoperator/）

| 方法 | 职责 |
|------|------|
| `Check` | 校验 baseline 非空 |
| `ReplacePackage` | 处理升级（与 Middleware 类似） |
| `TemplateParseWithBaseline` | 获取 OperatorBaseline -> 合并整个 Spec -> 渲染 -> HandlePreActions |
| `HandleResource` | 总控：模板解析 -> PreActions -> 额外资源 -> RBAC -> Deployment |
| `handleRBAC` | 遍历 permissions -> 生成 SA + Role/ClusterRole + RoleBinding/ClusterRoleBinding |
| `buildDeployment` | 反序列化 Deployment -> 设置 name/namespace/labels/ownerRef -> 创建/更新/删除 |
| `CompareDeployment` | 比较实际 Deployment 与期望的差异，不一致则恢复 |

#### middlewarepackage（pkg/service/middlewarepackage/）

| 方法 | 职责 |
|------|------|
| `Check` | 校验并更新状态 |
| `HandleSecret` | 处理 Secret 事件：创建 MiddlewarePackage -> 根据注解执行安装/卸载 |
| `HandleResource` | 从包中获取所有 Baseline/Configuration/Action -> 批量 Deploy/Delete（失败回滚） |

#### middlewareaction（pkg/service/middlewareaction/）

| 方法 | 职责 |
|------|------|
| `Check` | 校验 |
| `Execute` | 获取 ActionBaseline -> 逐步执行 CUE/CMD/HTTP |
| `HandlePreActions` | 遍历前置操作 -> 获取 ActionBaseline（必须是 PreAction 类型） -> ExecutePreAction |
| `ExecutePreAction` | 对 CUE 步骤执行映射和应用（修改传入的 unstructured 对象） |
| `executeCue` | 执行 CUE 步骤（KubectlGet/KubectlExec/KubectlEdit） |
| `executeCmd` | 执行 CMD 步骤（通过 `sh -c` 执行命令） |
| `executeHTTP` | 执行 HTTP 步骤（支持自定义 method/url/header/body） |
| `TemplateParseWithBaseline` | 对 Step 的 CUE/CMD/HTTP 字段进行模板渲染 |

**Step 类型常量**：
- `KubectlGet` - 获取 K8s 资源
- `KubectlExec` - 在 Pod 容器中执行命令
- `KubectlEdit` - 编辑/创建 K8s 资源

#### middlewareconfiguration（pkg/service/middlewareconfiguration/）

| 方法 | 职责 |
|------|------|
| `Check` | 校验并更新状态 |
| `Get` | 从集群获取，或从包中获取并缓存 |
| `GetTemplateParsedMiddlewareConfigurations` | 获取并渲染所有 Configuration 模板 |
| `Handle` | 将渲染后的模板解析为 unstructured -> 判断是否 Namespaced -> 设置 labels/annotations/ownerRef -> 创建/更新/删除 |
| `Deploy` | 设置 labels + OwnerReference -> 创建资源 |

#### packages（pkg/service/packages/）

| 方法 | 职责 |
|------|------|
| `Get` | 从 Secret 读取 -> zstd 解压 -> tar 解析 -> 读取 metadata.yaml |
| `List` | 列出所有包（按标签过滤 Secret） |
| `GetMetadata` | 获取包元数据 |
| `GetConfigurations` | 获取包中所有 MiddlewareConfiguration |
| `GetMiddlewareBaselines` | 获取包中所有 MiddlewareBaseline |
| `GetMiddlewareOperatorBaselines` | 获取包中所有 MiddlewareOperatorBaseline |
| `GetMiddlewareActionBaselines` | 获取包中所有 MiddlewareActionBaseline |
| `Compress` | zstd 压缩 |
| `DeCompress` | zstd 解压 |

**包格式**：Secret.Data["package"] -> zstd 压缩 -> tar 归档
- `metadata.yaml` - 包元数据
- `crds/` - CRD 定义文件
- `baselines/` - Baseline YAML 文件
- `configurations/` - Configuration YAML 文件
- `actions/` - Action YAML 文件

#### customresource（pkg/service/customresource/）

| 方法 | 职责 |
|------|------|
| `HandleGvk` | 从 Middleware -> Baseline -> OperatorBaseline 获取 GVK |
| `GetNeedPublishCustomResource` | 构建要发布的 CR（unstructured） |
| `RestoreIfIllegalUpdate` | 检查 kubectl 修改并还原（通过 managedFields 判断） |

#### status（pkg/service/status/condition.go）

| 方法 | 职责 |
|------|------|
| `GetCondition` | 从 Conditions 列表获取指定类型的 Condition，不存在则初始化 |
| `Condition.Failed` | 设置 Condition 为 False + 对应的失败 Reason |
| `Condition.Success` | 设置 Condition 为 True + 对应的成功 Reason，Message="成功" |
| `Condition.SuccessWithMsg` | 设置 Condition 为 True + 自定义 Message |
| `ConditionInit` | 创建初始 Condition（Status=Unknown, Reason=Initing） |

#### consts（pkg/service/consts/）

定义所有常量：
- `HandleAction`：`publish` / `delete` / `update`
- Labels 和 Annotations 的 Key 常量
- 错误变量：`SameTypeMiddlewareExists`、`SameTypeMiddlewareOperatorExists`

---

## 7. Watcher / Syncer 机制

### 7.1 CR Watcher（pkg/service/watcher/customresource.go）

**CustomResourceWatcher 结构**：
- `GVK` - 监听的资源类型
- `Namespace` - 命名空间
- `StopChan` - 停止通道
- `Counter` - 引用计数（同 GVK+Namespace 的 Middleware 数量）

**工作流程**：

1. **启动时**：`StartCRWatcher` 在 main.go 中以 goroutine 启动
   - 等待缓存同步
   - 列出所有 Middleware
   - 为每个 Middleware 解析 GVK -> 启动 Informer + Synchronizer
   - 同一 GVK+Namespace 复用 Watcher（Counter++）

2. **运行时**：每次 Middleware 发布 CR 时在 `buildCustomResource` 中启动
   - 如果 Watcher 不存在：创建新 Watcher -> 启动 Informer
   - 如果 Watcher 已存在：校准 Counter

3. **事件处理**（`NewResourceEventHandlerFuncs`）：
   - **AddFunc**：记录日志
   - **UpdateFunc**：记录日志，比较 ResourceVersion
   - **DeleteFunc**：检查 OwnerReference 的 Middleware 是否存在
     - 不存在：关闭 Watcher + Synchronizer
     - 存在：自动重建 CR（清除 ResourceVersion 后 Create）

4. **关闭**：`CloseCRWatcher`
   - Counter == 1：close(StopChan) -> 从 Map 删除
   - Counter > 1：Counter--

### 7.2 Synchronizer（pkg/service/synchronizer/synchronizer.go）

**功能**：每秒定时同步 CR 状态到 Middleware.Status.CustomResources

**同步内容**：
1. 获取 CR 的 status -> 反序列化到 CustomResources
2. 提取 Phase（从 `phase` 或 `state` 字段）
3. 提取 Replicas（从 `replicas` 或 `size` 字段）
4. 提取 Reason
5. 收集关联资源（通过 OwnerReference 和 Label 匹配）：
   - StatefulSet、Deployment、DaemonSet、Pod、Service、PVC
6. 同步灾备信息（如果有 `disasterSyncer` / `dataSyncer` 注解）
7. 处理 Conditions（从 CR status 中多种 key 提取：conditions、sentinelConditions、proxyConditions 等）
8. 排序所有 Include 列表（按 Name 排序）
9. 使用 DeepEqual 比较是否有变化，有变化才更新

**Label 匹配逻辑**：使用 `GeneralLabelKeys` 列表匹配关联资源：
- `cluster-name`, `app`, `middleware.cn/app`, `esname`, `middleware_expose_middlewarename`, `app.kubernetes.io/instance`

**停止机制**：通过 `SyncCustomResourceStopChanMap` 管理，CR 删除时关闭对应的 stop channel

---

## 8. 升级触发机制

### 8.1 触发方式

在 Middleware 或 MiddlewareOperator 的 **Annotations** 中添加：
- `middleware.cn/update: <目标版本号>` - 触发升级
- `middleware.cn/baseline: <目标基线名称>` - 指定升级后的基线（仅 Middleware）

### 8.2 升级流程（以 Middleware 为例）

```
检测到 LabelUpdate 注解
    |
    v
加锁（updatingLocker sync.Mutex）
    |
    v
再次确认注解存在（防止并发）
    |
    v
设置 State=Updating -> 更新状态
    |
    v
Checked=True?
    |-- 否 --> 返回
    |-- 是 -->
        获取新包（通过 component + version 标签查找 MiddlewarePackage）
            |
            v
        新包数量 != 1 --> 错误
            |
            v
        从新包获取目标 Baseline
            |
            v
        更新 Middleware:
            - Spec.Baseline = 新 Baseline 名称
            - Labels[packageversion] = 新版本
            - Labels[packagename] = 新包名
            - 删除 update 注解
            |
            v
        等待包就绪（最多重试 3 次，每次等 5 秒）
            |
            v
        更新 Middleware 资源
            |
            v
        设置 Updating Condition
        defer: 成功 -> Updating=True / 失败 -> Updating=False
```

### 8.3 MiddlewareOperator 升级特殊处理

- 升级时保留 `Globe` 和 `PreActions`，其余 Spec 字段重置
- 重新从新 Baseline 获取模板

---

## 9. 删除流程与 Finalizer

### 9.1 Middleware 删除

当 Middleware 被删除时（在 Reconcile 中检测到 NotFound）：
1. 从 `MiddlewareCache` 获取缓存副本
2. 调用 `HandleResource(Delete)`：
   - 倒序删除 MiddlewareConfigurations 创建的额外资源
   - 删除 CustomResource
3. 清除缓存

### 9.2 MiddlewareOperator 删除

当 MiddlewareOperator 被删除时：
1. 从 `MiddlewareOperatorCache` 获取缓存副本
2. 调用 `HandleResource(Delete)`：
   - 倒序删除额外资源
   - 删除 RBAC 资源（SA、Role/ClusterRole、RB/CRB）
   - 删除 Deployment
3. 清除缓存

### 9.3 MiddlewarePackage 卸载

通过 Secret 的 `middleware.cn/uninstall` 注解触发：
1. 调用 `HandleResource(Delete)`
   - 删除所有 MiddlewareBaseline
   - 删除所有 MiddlewareOperatorBaseline
   - 删除所有 MiddlewareActionBaseline
   - 删除所有 MiddlewareConfiguration
2. 设置 `enabled=false`，删除 `uninstall` 注解

### 9.4 Secret 删除

Secret 被删除时：
1. 删除对应的 MiddlewarePackage

### 9.5 CR 被误删

当被 Watcher 监听的 CR 被删除时：
1. 检查 OwnerReference 的 Middleware 是否存在
2. 如果存在：自动重建 CR（清除 ResourceVersion 后重新 Create）
3. 如果不存在：关闭 Watcher 和 Synchronizer

### 9.6 Finalizer

当前代码中 **未使用 Finalizer**。删除处理依赖于：
- Reconcile 中的 NotFound 检测 + Cache 机制
- K8s 的 OwnerReference 级联删除（ControllerReference）
- Watcher 的 DeleteFunc 事件处理

---

## 10. 工具包（pkg/tools）

### 10.1 模板渲染（template.go）

#### Quoter 接口

所有需要模板渲染的 CRD 都实现此接口：

```go
type Quoter interface {
    GetName() string
    GetNamespace() string
    GetLabels() map[string]string
    GetConfigurations() []v1.Configuration
    GetAnnotations() map[string]string
    GetUnified() *runtime.RawExtension
    GetPreActions() []v1.PreAction
    GetMiddlewareName() string
}
```

#### TemplateValues 模板变量

| 字段 | 来源 | 说明 |
|------|------|------|
| `.Globe` | Quoter.GetUnified() + 必要字段 | 全局变量（Name, Namespace, Labels, Annotations, PackageName, MiddlewareName） |
| `.Necessary` | Quoter.GetUnified() | 必填参数 |
| `.Values` | Configuration.Values | 配置值 |
| `.Step` | Context("step") | 步骤输出（用于 Action 步骤间数据传递） |
| `.Capabilities.KubeVersion` | K8s ServerVersion | K8s 版本信息（Version, Major, Minor, GitVersion） |
| `.Parameters` | 渲染后的 Parameters | 参数（在 TemplateParseWithBaseline 中设置） |

#### 模板函数（funcs.go）

基于 Sprig 函数库，额外添加：
- `toToml` - 转 TOML
- `toYaml` / `fromYaml` / `fromYamlArray` - YAML 转换
- `toJson` / `fromJson` / `fromJsonArray` - JSON 转换
- `toCue` - 转 CUE 格式（支持 map/slice/string/bool/int/float/null 递归转换）

移除了安全敏感函数：`env`、`expandenv`

#### YAML Go Template 处理（yaml.go - ProcessYAMLGoTemp）

处理 YAML 序列化后 Go Template 语法被引号包裹的问题：
- `key: '{{...}}'` -> `key: {{...}}`（去除单引号）
- `key: "{{...}}"` -> `key: {{...}}`（去除双引号）
- `key: '"{{...}}"'` -> `key: '{{...}}'`（去除外层单引号和内层双引号）
- `key: "'{{...}}'"` -> `key: "{{...}}"`（去除外层双引号和内层单引号）

### 10.2 CUE 处理（cue.go）

`ParseAndAddCueFile(bi, fieldName, content)` - 解析 CUE 内容并添加到 build.Instance

### 10.3 TAR 操作（tar.go）

```go
type TarInfo struct {
    Name  string
    Files map[string][]byte  // key: 文件路径（去除顶层目录），value: 文件内容
}
```

- `ReadTarInfo(data)` - 读取 tar 归档中的所有文件
- `TarInfo.ReadFile(name)` - 按名称查找文件（模糊匹配，使用 strings.Contains）

### 10.4 JSON/Map 工具（json.go）

#### StructMerge 深度合并

支持两种模式：
- `StructMergeMapType` - Map 深度合并
- `StructMergeArrayType` - Array 合并

**Map 合并规则**：
- 递归合并嵌套 map
- 递归合并嵌套 array
- 其他类型：new 覆盖 old

**Array 合并规则**：
- 如果元素是 map：按 `ArrayStructKey`（`["name", "serviceAccountName"]`）匹配
  - 找到同名元素：递归合并
  - 未找到同名元素：append
  - 无结构化 key：按索引合并
- 其他类型：直接用 new 替换 old

#### CompareJson 比较

只比对 old 中存在的 key，支持结构体数组按 name 排序后比较

#### ExtractJsonKey 提取

将嵌套 JSON 展平为 `key.subkey.index` 格式的扁平 map

### 10.5 其他工具

- **gvk.go**：GVK <-> String 互转（格式 `group/version/kind`）
- **array.go**：`IsExistInStringSlice` - 字符串切片查找
- **map.go**：`JsonToMap` - JSON bytes -> map

---

## 11. K8s 操作封装（pkg/k8s）

### 11.1 Client 工厂（kubeClient/client.go）

| 方法 | 返回类型 | 用途 |
|------|---------|------|
| `GetDynClient` | `*dynamic.DynamicClient` | 动态客户端（Informer 使用） |
| `GetRuntimeClient` | `client.Client` | controller-runtime 客户端 |
| `GetApiextensionsv1Client` | `*apiextclientset.Clientset` | CRD 操作客户端 |
| `GetClientSet` | `*kubernetes.Clientset` | 标准客户端集（Pod exec 使用） |
| `GetDiscoveryClient` | `*discovery.DiscoveryClient` | 资源发现客户端 |

### 11.2 CRD 资源 CRUD

每个 CRD 资源都有对应的 k8s 操作文件，提供统一的 CRUD 接口：

| 资源 | 文件 | 作用域 | 缓存 | 状态更新 |
|------|------|--------|------|----------|
| Middleware | middleware.go | Namespaced | `MiddlewareCache` (sync.Map) | RetryOnConflict + DeepEqual 跳过 |
| MiddlewareOperator | middlewareoperator.go | Namespaced | `MiddlewareOperatorCache` | RetryOnConflict + DeepEqual 跳过 |
| MiddlewareBaseline | middlewarebaseline.go | Cluster | 无 | RetryOnConflict |
| MiddlewareOperatorBaseline | middlewareoperatorbaseline.go | Cluster | 无 | RetryOnConflict |
| MiddlewarePackage | middlewarepackage.go | Cluster | `MiddlewarePackageCache` | RetryOnConflict |
| MiddlewareAction | middlewareaction.go | Namespaced | `MiddlewareActionCache` | RetryOnConflict |
| MiddlewareActionBaseline | middlewareactionbaseline.go | Cluster | 无 | RetryOnConflict |
| MiddlewareConfiguration | middlewareconfiguration.go | Cluster | 无 | RetryOnConflict |

### 11.3 原生资源操作

| 文件 | 资源 | 操作 |
|------|------|------|
| deployment.go | Deployment | Create, CreateOrUpdate, Update, Get, GetList, Delete, CompareSpec |
| secret.go | Secret | Create, Delete, Update, Get, GetList |
| rbac.go | ServiceAccount, Role, ClusterRole, RoleBinding, ClusterRoleBinding | Create, CreateOrUpdate, Update, Get, Delete |
| customresource.go | Unstructured | Create, CreateOrUpdate, Update, Get, List, Delete, IsNamespaced |
| sts.go | StatefulSet | GetList |
| pod.go | Pod | GetList |
| pvc.go | PVC | GetList |
| svc.go | Service | GetList |
| daemonsets.go | DaemonSet | GetList |
| replicaSet.go | ReplicaSet | GetList |

### 11.4 Informer（informer.go）

`NewInformerOptUnit` - 创建并运行 Informer：
- 使用 DynamicClient 的 ListWatch
- 30 秒 resync
- 自定义 Indexer：apiVersion、kind
- panic 自动恢复重启
- 通过 `GetGroupVersionResource` 将 GVK 转换为 GVR

### 11.5 Patch 工具（patch.go）

支持三种 Patch 类型：
- `JSONPatchType` - JSON Patch
- `MergePatchType` - JSON Merge Patch
- `StrategicMergePatchType` - Strategic Merge Patch

提供 `CreatePatch`、`ApplyPatch`、`ValidatePatch` 方法

### 11.6 Pod Exec（k8s.go）

`ExecCommandInContainer` - 在 Pod 容器中执行命令（通过 SPDY Executor）

---

## 12. 关键设计决策

| 决策 | 理由 |
|------|------|
| 使用 sync.Map 缓存 Baseline/Configuration/OperatorBaseline/ActionBaseline | 避免频繁从 Secret 解压读取包内容；配合定时清理（cache_cleanup_interval，默认 1800 秒） |
| Middleware 删除时从 Cache 获取对象 | 已删除的对象无法从 API Server 获取，需要依赖缓存进行资源清理 |
| Middleware Controller 使用 Predicate 过滤 Status 更新 | 避免 Status 更新触发无限循环 Reconcile |
| MiddlewareAction 的一次性执行语义（State 非空即跳过） | Action 是操作而非声明式状态，执行完毕后不应重复执行 |
| CR 被删除时自动重建 | 保护中间件实例不被意外删除；通过 Watcher DeleteFunc 实现 |
| MiddlewareOperator 比较 Deployment 差异并恢复 | 防止 Deployment 被外部修改（如 kubectl edit），确保与 Spec 声明一致 |
| 升级时使用 sync.Mutex 加锁 | 防止并发升级导致的竞态条件 |
| MiddlewarePackage 安装时禁用同组件其他已启用包 | 确保同一中间件类型只有一个激活版本 |
| 倒序删除 Configuration 资源 | 避免依赖问题（先创建的可能被后创建的依赖） |
| HandleResource Publish 失败时回滚已发布资源 | 保持事务性，避免部分资源残留 |
| 使用 Go Template + Sprig + 自定义函数 | 兼容 Helm 模板语法，降低学习成本 |
| 使用 CUE 语言进行字段映射和资源编排 | CUE 提供更强的类型约束和数据校验能力 |
| Secret 作为包存储介质 | 利用 K8s 原生存储，无需额外存储组件；zstd 压缩减小体积 |
| OwnerReference 级联删除 | 利用 K8s GC 自动清理子资源，减少手动清理代码 |
| 状态更新使用 RetryOnConflict | 处理并发更新冲突，保证最终一致性 |
| Middleware/MO 状态更新使用 DeepEqual 跳过 | 减少不必要的 API 调用 |
| MiddlewareAction 定时清理（600 秒检查一次） | 清理超过 cache_cleanup_interval 的过期 Action |
