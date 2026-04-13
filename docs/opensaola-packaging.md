# Dataservice-Baseline 包适配技术文档

## 目录

- [1. 架构概览](#1-架构概览)
  - [1.1 dataservice-baseline 定位与作用](#11-dataservice-baseline-定位与作用)
  - [1.2 中间件包目录结构约定](#12-中间件包目录结构约定)
  - [1.3 各组成部分关系图](#13-各组成部分关系图)
  - [1.4 与 OpenSaola 的交互流程](#14-与-OpenSaola-的交互流程)
- [2. metadata.yaml 规范](#2-metadatayaml-规范)
  - [2.1 字段完整说明](#21-字段完整说明)
- [3. Baselines 体系](#3-baselines-体系)
  - [3.1 Baseline 的定义和作用](#31-baseline-的定义和作用)
  - [3.2 Baseline 文件格式详解](#32-baseline-文件格式详解)
  - [3.3 Baseline 值的合并逻辑](#33-baseline-值的合并逻辑)
- [4. Configurations 体系](#4-configurations-体系)
  - [4.1 Configuration 文件格式和加载机制](#41-configuration-文件格式和加载机制)
  - [4.2 Configuration 类型分类](#42-configuration-类型分类)
  - [4.3 Go 模板语法使用](#43-go-模板语法使用)
  - [4.4 模板变量来源和渲染流程](#44-模板变量来源和渲染流程)
- [5. Actions 系统](#5-actions-系统)
  - [5.1 Action 定义格式和字段说明](#51-action-定义格式和字段说明)
  - [5.2 Action 类型分类](#52-action-类型分类)
  - [5.3 Action 执行机制](#53-action-执行机制)
- [6. CRD 定义](#6-crd-定义)
  - [6.1 各中间件自带 CRD](#61-各中间件自带-crd)
  - [6.2 与 OpenSaola CRD 的关系](#62-与-OpenSaola-crd-的关系)
- [7. Manifests 目录](#7-manifests-目录)
  - [7.1 middlewareversionrule 格式和用途](#71-middlewareversionrule-格式和用途)
  - [7.2 parameters 文件（表单参数定义）](#72-parameters-文件表单参数定义)
  - [7.3 i18n 国际化](#73-i18n-国际化)
  - [7.4 hiddenmenus 配置](#74-hiddenmenus-配置)
- [8. 渲染覆盖优先级](#8-渲染覆盖优先级)
  - [8.1 各层级值的来源](#81-各层级值的来源)
  - [8.2 覆盖优先级链（从低到高）](#82-覆盖优先级链从低到高)
  - [8.3 合并策略（深度合并 vs 替换）](#83-合并策略深度合并-vs-替换)
  - [8.4 模板变量解析顺序](#84-模板变量解析顺序)
  - [8.5 冲突处理策略](#85-冲突处理策略)
  - [8.6 实际示例](#86-实际示例)
- [9. Secret 结构与标签](#9-secret-结构与标签)
  - [9.1 connection-credential 常见模式](#91-connection-credential-常见模式)
  - [9.2 各中间件 Secret data 字段对照](#92-各中间件-secret-data-字段对照)
  - [9.3 Labels 和 Annotations](#93-labels-和-annotations)
- [10. Redis 完整适配案例](#10-redis-完整适配案例)
  - [10.1 Redis 包概述](#101-redis-包概述)
  - [10.2 Redis Baselines 详解（8 个）](#102-redis-baselines-详解8-个)
  - [10.3 Redis Configurations 详解（14 个）](#103-redis-configurations-详解14-个)
  - [10.4 Redis Actions 详解（19 个）](#104-redis-actions-详解19-个)
  - [10.5 Redis CRD](#105-redis-crd)
  - [10.6 Redis Manifests](#106-redis-manifests)
  - [10.7 Redis 部署架构总结](#107-redis-部署架构总结)
- [11. 与 OpenSaola 的交互接口](#11-与-OpenSaola-的交互接口)
  - [11.1 包如何被 OpenSaola 消费](#111-包如何被-OpenSaola-消费)
  - [11.2 渲染流程中 operator 的角色](#112-渲染流程中-operator-的角色)
  - [11.3 状态同步](#113-状态同步)
- [12. 包开发最佳实践](#12-包开发最佳实践)

---

## 1. 架构概览

### 1.1 dataservice-baseline 定位与作用

dataservice-baseline 是数据服务平台的中间件包仓库，提供标准化的配置单元，用于在 Kubernetes 上部署和管理中间件（如 Redis、MySQL、MinIO 等）。通过声明式 YAML 文件，实现中间件的自动化交付和全生命周期管理。

核心职责：
- 定义中间件的部署模式（Baselines）
- 定义运行时所需的 Kubernetes 资源模板（Configurations）
- 定义运维操作（Actions）
- 定义自定义资源（CRDs）
- 提供平台侧参数配置和国际化（Manifests）

### 1.2 中间件包目录结构约定

```
middleware-name/
  metadata.yaml              # [必需] 包元数据（Package Metadata）
  icon.svg                   # [推荐] 包图标
  baselines/                 # [必需] 基线配置（Baseline Configurations）
    operator-*.yaml          #   [可选] Operator 基线 (MiddlewareOperatorBaseline)
    *.yaml                   #   [必需] 实例基线 (MiddlewareBaseline)
  configurations/            # [可选] 配置模板（Configuration Templates）
    *.yaml                   #   MiddlewareConfiguration 资源
  crds/                      # [可选] CRD 定义（Custom Resource Definitions）
    *.yaml                   #   包装在 MiddlewareConfiguration 中的 CRD
  actions/                   # [可选] 动作配置（Action Definitions）
    pre-*.yaml               #   前置动作 (PreAction)
    *.yaml                   #   运维/普通动作 (OpsAction / NormalAction)
  manifests/                 # [可选] 参数清单（Parameter Manifests）
    *parameters.yaml         #   参数定义（表单字段元数据）
    i18n.yaml                #   国际化翻译
    middlewareversionrule.yaml  # 版本升降级规则
    hiddenmenus.yaml         #   隐藏菜单项
```

最小化包只需 `metadata.yaml` + 1 个 baseline 文件即可发布。

### 1.3 各组成部分关系图

```
                   metadata.yaml
                       |
                       | (包信息)
                       v
            +---------------------+
            |    baselines/       |
            |                     |
            |  OperatorBaseline   |-----> configurations/ (CRDs, Operator Service, etc.)
            |       |             |-----> actions/ (pre-operator-*)
            |       |             |
            |  InstanceBaseline   |-----> configurations/ (ConfigMap, Secret, Alert, etc.)
            |       |             |-----> actions/ (pre-*, ops-*)
            +---------------------+
                       |
                       | (参考匹配: metadata.name)
                       v
               manifests/ (平台侧)
               - parameters (表单参数)
               - i18n (国际化)
               - middlewareversionrule (版本规则)
```

**引用关系核心原则**：所有引用通过 `metadata.name` 字段匹配，而非文件名。

### 1.4 与 OpenSaola 的交互流程

1. **包发布**：中间件包通过 saola-cli 打包成 TAR 格式的 Secret，上传到 Kubernetes 集群。
2. **包解析**：OpenSaola 解析 Secret 中的包内容，提取 baselines、configurations、actions 等。
3. **实例创建**：用户通过平台创建中间件实例时，OpenSaola 根据选定的 baseline 渲染参数和配置。
4. **资源渲染**：
   - 将用户输入（Necessary）与 baseline 定义的 Parameters 合并
   - 通过 configurations[].values 将参数注入 Configuration 模板
   - 使用 Go 模板引擎渲染最终的 Kubernetes 资源
5. **资源创建**：渲染后的 Kubernetes 资源被 apply 到集群中。
6. **状态同步**：OpenSaola 持续监控中间件 CRD 的状态，同步到 Middleware 资源的 status 中。

---

## 2. metadata.yaml 规范

### 2.1 字段完整说明

| 字段名 | 类型 | 必填 | 说明 | 边界条件 |
|--------|------|------|------|----------|
| `name` | string | 是 | 中间件名称，用于平台展示 | 不能为空，建议首字母大写 |
| `version` | string | 是 | 包版本号，格式 `<中间件版本>-<包主版本>.<次版本>.<修订版>` | 必须符合格式规范，如 `2.19.2-1.0.0` |
| `app.version` | []string | 是 | 中间件支持的版本列表 | 至少包含一个版本号 |
| `app.deprecatedVersion` | []string | 否 | 已废弃的版本列表 | 可选，用于版本迁移提示 |
| `owner` | string | 是 | 供应商/开发者名称 | 不能为空 |
| `type` | string | 是 | 中间件类型分类 | 可选值：`db` / `cache` / `mq` / `search` / `storage` |
| `chartType` | string | 否 | Chart 类型标识（可选/遗留字段，当前版本的 OpenSaola 未解析此字段） | 通常与 name 相同 |
| `description` | string | 是 | 包描述信息 | 简要描述中间件功能 |
| `changes` | string | 否 | 变更日志（可选/遗留字段，当前版本的 OpenSaola 未解析此字段） | 多行文本，记录版本变更历史 |

---

## 3. Baselines 体系

### 3.1 Baseline 的定义和作用

Baseline（基线）是中间件部署模式的完整定义，包含两种类型：

1. **MiddlewareOperatorBaseline**（Operator 基线）：定义 Operator 控制器的部署配置，包括 GVK、全局变量（globe）、RBAC 权限、Deployment 配置等。
2. **MiddlewareBaseline**（实例基线）：定义中间件实例的部署模式，包括用户输入参数（necessary）、实例资源配置（parameters）、前置动作（preActions）、配置模板引用（configurations）等。

每个实例基线通过 `operatorBaseline.name` 引用一个 Operator 基线（有 Operator 模式），或直接定义 `gvk` 字段（无 Operator 模式）。

### 3.2 Baseline 文件格式详解

#### MiddlewareOperatorBaseline 关键字段

```yaml
apiVersion: middleware.cn/v1
kind: MiddlewareOperatorBaseline
metadata:
  name: <唯一名称>          # 被实例基线通过 operatorBaseline.name 引用
  annotations:
    baselineName: <展示名>   # 平台 UI 展示名称
    description: <描述>      # 基线说明
spec:
  gvks:                      # 支持的 API 资源类型
    - name: <GVK名称>        # 被引用时的 gvkName
      group: <API Group>
      version: <API Version>
      kind: <Resource Kind>
  globe:                     # 全局配置（小写字段，模板中用 .Globe.<字段名>）
    repository: <镜像仓库>
    project: <项目名>
  preActions: []             # Operator 前置动作
  permissionScope: <Cluster|Namespace>  # 权限范围
  permissions: []            # RBAC 权限规则
  deployment:                # Operator Deployment 配置
    spec: ...
  configurations: []         # 引用的配置模板
```

#### MiddlewareBaseline 关键字段

```yaml
apiVersion: middleware.cn/v1
kind: MiddlewareBaseline
metadata:
  name: <唯一名称>
  labels: {}
  annotations:
    baselineName: <展示名>
    description: <描述>
    mode: <HA|空>            # 标记高可用模式
    active: <"2"|空>         # 标记双活模式
    datapath: <数据路径模板>
spec:
  operatorBaseline:          # 有 Operator 模式
    name: <Operator基线名>
    gvkName: <GVK名称>
  gvk:                       # 无 Operator 模式（与 operatorBaseline 二选一）
    group: <API Group>
    version: <API Version>
    kind: <Resource Kind>
  necessary: {}              # 用户输入参数定义（JSON 格式的表单字段）
  preActions: []             # 前置动作列表
  parameters: {}             # 实例资源配置（传递给 CRD 或原生 K8s 资源）
  configurations: []         # 引用的配置模板及注入值
```

### 3.3 Baseline 值的合并逻辑

Baseline 中各字段的合并优先级详见 [8. 渲染覆盖优先级](#8-渲染覆盖优先级)。

**合并策略概要**：
- OperatorBaseline 的 `globe` 字段被系统注入到 `.Globe` 命名空间下（小写字段），仅在 Operator 上下文中可用
- 系统自动注入 6 个大写字段（Name, Namespace, Labels, Annotations, PackageName, MiddlewareName）到 `.Globe` 命名空间，所有上下文均可用
- `parameters` 中的模板变量在渲染时被替换为 `.Necessary` 中用户的实际输入值
- `configurations[].values` 中的模板变量同样被替换，然后作为 `.Values` 注入到 Configuration 模板中

---

## 4. Configurations 体系

### 4.1 Configuration 文件格式和加载机制

每个 Configuration 文件是一个 `MiddlewareConfiguration` 类型的 YAML，包含：

```yaml
apiVersion: middleware.cn/v1
kind: MiddlewareConfiguration
metadata:
  name: <唯一名称>        # 被 baseline 的 configurations[].name 引用
spec:
  template: |             # Go Template 格式的 Kubernetes 资源模板
    <K8s 资源 YAML>
```

**加载机制**：
1. Baseline 的 `configurations[]` 列表声明要引用哪些 Configuration
2. 系统根据 `configurations[].name` 查找匹配 `metadata.name` 的 Configuration 文件
3. 将 `configurations[].values` 注入为 `.Values` 变量
4. 使用 Go 模板引擎渲染 `spec.template` 内容
5. 渲染结果作为 Kubernetes 资源 apply 到集群

### 4.2 Configuration 类型分类

按用途分类：

| 类型 | 说明 | 示例 |
|------|------|------|
| CRD 定义 | 自定义资源定义 | crd-redisclusters, crd-mysqlcluster |
| 连接凭证 | 生成连接 Secret | redis-credential, mysql-connection-credential |
| 配置文件 | ConfigMap 形式的运行时配置 | redis-config, mysql-config |
| 监控相关 | ServiceMonitor / PrometheusRule | redis-servicemonitor, mysql-alertrule |
| 服务暴露 | Service / Ingress | redis-operator-service, minio-service |
| 监控面板 | Grafana Dashboard ConfigMap | redis-operator-dashboard, minio-dashboard |
| 存储类 | StorageClass 多存储映射 | redis-sc, mysql-sc |
| 其他 | ServiceAccount / Job 等 | minio-serviceaccount, minio-setup |

按上下文分类：

| 上下文 | 可用变量 | 典型文件 |
|--------|---------|---------|
| Operator 上下文 | `.Globe.*`（含 repository/project）+ `.Values.*` | operator-service, operator-dashboard, CRD |
| Instance 上下文 | `.Globe.*`（不含 repository/project）+ `.Values.*` | redis-config, mysql-config, alertrule |

### 4.3 Go 模板语法使用

Configuration 模板中常用的 Go 模板语法：

**基础变量引用**：
- `{{ .Globe.Name }}`：实例名称
- `{{ .Globe.Namespace }}`：命名空间
- `{{ .Values.xxx }}`：从 baseline 注入的参数

**条件渲染**：
- `{{- if .Values.xxx }}...{{- end }}`
- `{{- if eq .Values.type "sentinel" }}...{{- end }}`

**循环**：
- `{{- range $key, $value := .Globe.Labels }}{{ $key }}: {{ $value }}{{- end }}`
- `{{- range $i := until (int .Values.redis.replicas) }}...{{- end }}`

**函数**：
- `{{ .Values.xxx | default 1000 }}`：默认值
- `{{ toYaml . | nindent 12 }}`：YAML 序列化并缩进
- `{{ b64dec .Values.encodePassword }}`：Base64 解码
- `{{ semverCompare ">=6.2.6" .Values.version }}`：语义版本比较
- `{{ contains "," .Values.storageClassName }}`：字符串包含判断
- `{{ toCue . }}`：转换为 CUE 格式（用于 Action 步骤）

**能力检测**：
- `{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}`：检测集群中是否安装了对应 API

**自定义 define/include**：
- `{{- define "redis.cal.memory" -}}...{{- end -}}`：定义复用模板
- `{{ include "redis.maxMemory" . }}`：调用自定义模板

### 4.4 模板变量来源和渲染流程

```
用户输入 (Middleware.spec.necessary)
    |
    v
MiddlewareBaseline.spec.necessary （定义字段和默认值）
    |
    +---> .Necessary.* （用于 parameters 和 configurations[].values）
    |
    v
MiddlewareBaseline.spec.parameters （渲染 .Necessary 引用）
    |
    +---> .Parameters.* （用于 configurations[].values）
    |
    v
MiddlewareBaseline.spec.configurations[].values （渲染 .Necessary 和 .Parameters 引用）
    |
    +---> .Values.* （注入 Configuration 模板）
    |
    v
MiddlewareConfiguration.spec.template （只能访问 .Values 和 .Globe）
    |
    v
最终的 Kubernetes 资源 YAML
```

---

## 5. Actions 系统

### 5.1 Action 定义格式和字段说明

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `metadata.name` | string | 是 | Action 唯一标识，被 baseline 的 preActions[].name 引用 |
| `metadata.annotations.alias` | string | 否 | 平台 UI 显示名称 |
| `metadata.annotations.ignore` | string | 否 | "true" 时在 UI 中隐藏 |
| `metadata.annotations.roles` | string | 否 | 适用的角色列表，逗号分隔 |
| `spec.baselineType` | string | 是 | `PreAction` / `NormalAction`（源码仅定义这两个常量；`OpsAction` 不是源码中的显式类型，但实际 YAML 中使用了此值（如 restart.yaml），控制器通过 `!= PreAction` 的排除逻辑使其正常工作） |
| `spec.actionType` | string | 是 | 动作类型标识，如 affinity / tolerations / restart / failover / expose / migrate |
| `spec.supportedBaselines` | []string | 否 | 限定此 Action 仅适用于特定基线 |
| `spec.necessary` | map | 否 | Action 的输入参数定义 |
| `spec.steps` | []Step | 是 | 执行步骤列表 |

**Step 字段**：

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 步骤名称，用于 `.Step.<name>` 引用 |
| `type` | string | 步骤类型：`KubectlEdit` / `KubectlGet` / `KubectlExec`（使用 cue 字段时必填） |
| `cue` | string | CUE 表达式，定义资源编辑/获取操作 |
| `cmd` | object | Shell 命令定义（无需 type 字段） |
| `http` | object | HTTP 请求定义（无需 type 字段） |
| `output.type` | string | 输出类型：`string` / `json` / `yaml` |
| `output.expose` | bool | 是否暴露给后续步骤 |

### 5.2 Action 类型分类

**PreAction（前置动作）**：实例创建前执行，用于配置亲和性、容忍度、日志等。通过 baseline 的 `preActions[]` 引用。

**OpsAction（运维操作动作）**：运维时执行的操作，如重启、故障转移、节点迁移。

**NormalAction（普通动作）**：其他类型操作，如服务暴露、数据安全配置、参数设置。

### 5.3 Action 执行机制

1. **PreAction 执行时机**：在创建 Middleware 实例时，按 baseline 中 `preActions[]` 的声明顺序执行
2. **参数来源**：
   - PreAction：来自 `preActions[].parameters` + `Middleware.spec.necessary`
   - OpsAction/NormalAction：来自 `MiddlewareAction.spec.necessary`（用户执行时输入）
3. **步骤执行**：按 `steps[]` 数组顺序依次执行，步骤间通过 `.Step.<name>.output` 传递数据
4. **CUE 步骤**：KubectlEdit 步骤中的 `output` 块定义要 patch 的资源内容，`resource` 块定义目标资源
5. **CMD 步骤**：`command` 数组用空格连接后通过 `sh -c` 执行

---

## 6. CRD 定义

### 6.1 各中间件自带 CRD

**Redis CRDs（2 个）**：

| 文件 | metadata.name | CRD 全名 | Kind | 说明 |
|------|---------------|----------|------|------|
| redis-crd.yaml | crd-redisclusters | redisclusters.redis.middleware.hc.cn | RedisCluster | Redis 集群主 CRD |
| shake-crd.yaml | crd-redisshakes | redisshakes.redis.middleware.hc.cn | RedisShake | Redis 数据同步（RedisShake）CRD |

**MySQL CRDs（4 个）**：

| 文件 | metadata.name | CRD 全名 | Kind | 说明 |
|------|---------------|----------|------|------|
| mysqlcluster.yaml | crd-mysqlcluster | mysqlclusters.mysql.middleware.harmonycloud.cn | MysqlCluster | MySQL 集群主 CRD |
| replicate.yaml | crd-mysqlreplicate | mysqlreplicates.mysql.middleware.harmonycloud.cn | MysqlReplicate | MySQL 复制 CRD |
| mysqlbackup-crd.yaml | crd-mysqlbackup | mysqlbackups.mysql.middleware.harmonycloud.cn | MysqlBackup | MySQL 备份 CRD |
| mysqlbackupschedule-crd.yaml | crd-mysqlbackupschedule | mysqlbackupschedules.mysql.middleware.harmonycloud.cn | MysqlBackupSchedule | MySQL 定时备份 CRD |

**MinIO CRDs**：无（使用无 Operator 模式，直接部署原生 StatefulSet）

### 6.2 与 OpenSaola CRD 的关系

CRD 文件被包装在 `MiddlewareConfiguration` 中，通过 OperatorBaseline 的 `configurations[]` 引用。在 Operator 部署时，这些 CRD 被 apply 到集群，供 Operator 控制器使用。

CRD 中的特殊 annotations 定义了状态映射逻辑：
- `harmonycloud-middleware-status-phase`：JQ 表达式，用于从 CRD 对象的 annotations 推断 phase 状态
- `harmonycloud-middleware-status-process`：从 annotations 提取进度信息
- `harmonycloud-middleware-status-log`：从 annotations 提取日志信息

OpenSaola 利用这些 annotations 将中间件 CRD 的状态同步到 `Middleware` 资源的 status 中。

---

## 7. Manifests 目录

### 7.1 middlewareversionrule 格式和用途

版本规则文件定义了中间件版本的分组和升降级路径：

```yaml
mainVersionGroups:          # 版本分组
  "<组名>":                 # 组标识
    - "<具体版本>"          # 属于该组的具体版本列表

crossUpgradeRules:          # 跨版本升级规则
  "<源组>":
    - "<目标组>"            # 允许升级到的目标组

crossDowngradeRules:        # 跨版本降级规则
  "<源组>":
    - "<目标组>"            # 允许降级到的目标组
```

### 7.2 parameters 文件（表单参数定义）

Parameters 文件定义了平台界面上可配置的参数元数据，供前端生成配置表单。

```yaml
middlewareDefinition: <baseline名称>   # 对应 MiddlewareBaseline 的 metadata.name
values:
  - name: <组名>
    path: spec|configurations|<config名>|values|args  # 参数在 Middleware 中的路径
    parameters:
      - <参数名>:
          - name: default
            value: <默认值>
          - name: isReboot
            value: <y|n>         # 是否需要重启生效
          - name: range
            value: <取值范围>     # 如 [0-100000] 或 [no|yes]
          - name: describe
            value: <参数描述>
```

### 7.3 i18n 国际化

i18n.yaml 定义了需要翻译的字符串对照表：

```yaml
baselineName:               # 基线名称翻译
  - name: "<英文>"
    name-zh: "<中文>"

description:                # 描述翻译
  - name: "<英文>"
    name-zh: "<中文>"

placeholder:                # 占位符翻译
label:                      # 标签翻译
actionAliasName:            # Action 别名翻译
```

### 7.4 hiddenmenus 配置

仅 MinIO 使用了 `hiddenmenus.yaml`，定义了平台 UI 中需要隐藏的菜单项：

```yaml
hiddenMenus:
  - dataSecurity       # 隐藏数据安全菜单
  - disasterBackup     # 隐藏灾备菜单
  - parameter          # 隐藏参数配置菜单
```

Redis 和 MySQL 未使用此文件。

---

## 8. 渲染覆盖优先级

### 8.1 各层级值的来源

中间件包的渲染过程涉及多个层级的值：

| 层级 | 来源 | 用途 | 示例 |
|------|------|------|------|
| 系统注入 Globe | OpenSaola 自动注入 | 提供实例元数据 | `.Globe.Name`、`.Globe.Namespace` |
| Operator Globe | OperatorBaseline.spec.globe | 提供镜像仓库等全局配置 | `.Globe.repository`、`.Globe.project` |
| Necessary | 用户创建实例时填写 | 用户可控参数 | `.Necessary.password`、`.Necessary.resource.mysql.replicas` |
| Parameters | MiddlewareBaseline.spec.parameters | 实例资源定义 | `.Parameters.type`、`.Parameters.replicas` |
| Values | Baseline.configurations[].values | Configuration 模板参数 | `.Values.args.maxmemory` |
| Capabilities | 系统环境能力检测 | K8s 集群能力 | `.Capabilities.APIVersions`、`.Capabilities.KubeVersion` |

### 8.2 覆盖优先级链（从低到高）

```
  1. Baseline 中的硬编码默认值（最低优先级）
     |
  2. Baseline.spec.parameters 中定义的默认值
     |
  3. Configuration 模板中的 | default 默认值
     |
  4. Baseline.configurations[].values 中注入的值
     |
  5. 用户通过 Middleware.spec.necessary 填写的值
     |
  6. PreAction 对 Middleware 资源的 CUE patch（最高优先级）
```

> **关键说明**：源码执行顺序为 TemplateParseWithBaseline（解析 Necessary 并渲染 Parameters）→ HandlePreActions（执行 CUE patch）→ handleExtraResource（渲染 Configuration）。PreAction 的 CUE patch 通过 MergeMap 将输出深度合并到 Middleware 对象上，可以覆盖 Necessary 推导的值，因此 PreAction patch 实际优先级高于用户 Necessary。

**详细说明**：

**第 1 层：Baseline 硬编码值**
- Baseline 文件中直接写定的值，如 `type: sentinel`、`servicePort: 6379`
- 这些值不会被用户覆盖，是部署模式的固有特征

**第 2 层：Parameters 默认值**
- Parameters 中引用 `.Necessary.*` 的字段，如果 Necessary 中的 JSON 定义包含 `"default"` 值，且用户未填写，则使用默认值
- 例如：`replicas: "{{ .Necessary.resource.redis.replicas }}"` 中，Necessary 定义了 `"default": 2`

**第 3 层：Configuration 模板 default**
- 模板中使用 `{{ .Values.xxx | default <值> }}` 提供的后备默认值
- 仅当 `.Values.xxx` 为空/nil 时生效
- 例如：`maxclients: {{ .Values.args.maxclients | default 15000 }}`

**第 4 层：Baseline configurations[].values**
- Baseline 向 Configuration 注入的参数值
- 可以包含固定值或模板引用
- 例如：`args: { maxmemory-policy: volatile-lru }`

**第 5 层：用户输入 Necessary**
- 用户在创建实例时填写的参数值
- 覆盖 Necessary JSON 定义中的 default 值
- 通过 `{{ .Necessary.xxx }}` 在 parameters 和 configurations[].values 中被引用

**第 6 层：PreAction CUE patch**
- PreAction 在 HandlePreActions 阶段执行，晚于 Necessary 的解析
- PreAction 通过 KubectlEdit 步骤中的 CUE 表达式对 Middleware 资源进行 patch
- patch 结果通过 MergeMap 深度合并到 Middleware 对象上，可以覆盖 Necessary 推导出的 parameters 值（如亲和性、容忍度配置）
- exposed=true 的 PreAction 允许用户修改参数
- exposed=false 的 PreAction 使用 baseline 中定义的固定参数

### 8.3 合并策略（深度合并 vs 替换）

| 场景 | 合并策略 | 说明 |
|------|---------|------|
| PreAction KubectlEdit output | 深度合并（Patch） | CUE output 块中的字段与目标资源做 JSON merge patch |
| configurations[].values | 数组深度合并（MergeArray） | map 类型元素按 `name` 或 `serviceAccountName` 字段匹配后深度合并；无匹配键时按位置索引合并；未匹配的元素追加到数组末尾；非 map 类型数组则整体替换 |
| Necessary 用户输入 | 替换 | 用户填写的值直接替换 JSON 定义中的 default 值 |
| Go 模板 `| default` | 后备值 | 仅当原值为空/nil/false 时使用 default 值 |

> **StructMerge 合并方向**：
> - 调用方式：`StructMerge(old=baseline, new=Middleware)`
> - Middleware 中已有的值覆盖 baseline 值（Middleware 优先）
> - 当 Middleware 字段为 null/空时，使用 baseline 值填充
> - 合并结果写回 Middleware 对象
> - 此规则适用于 parameters、configurations、labels、annotations 等所有合并场景

### 8.4 模板变量解析顺序

变量解析发生在以下阶段，按顺序执行：

**阶段 1：TemplateParseWithBaseline**
- 合并 baseline 和 Middleware 的 parameters、configurations、labels、annotations、preActions
- 渲染 parameters 模板（解析 .Necessary.* 引用）
- 渲染 configurations[].values 模板（解析 .Necessary.* 和 .Parameters.* 引用）
- 渲染 metadata 模板（labels 和 annotations 中的 Go 模板表达式也在此阶段解析）

**阶段 2：HandlePreActions**
- 执行 PreAction 步骤，可通过 CUE patch 修改 Middleware 的 parameters
- 注意：此阶段不会重新渲染 configurations[].values，那些值已在阶段 1 定型

**阶段 3：handleExtraResource + buildCustomResource**
- 渲染 MiddlewareConfiguration.spec.template，使用阶段 1 已渲染的 configurations[].values 作为 .Values
- 构建 CR，将阶段 2 修改后的 parameters 整体作为 CR 的 spec 字段发布

### 8.5 冲突处理策略

| 冲突类型 | 处理方式 |
|---------|---------|
| Necessary default vs 用户输入 | 用户输入优先 |
| Parameters 固定值 vs Necessary 引用 | Necessary 引用的运行时值优先 |
| Configuration `| default` vs values 注入 | values 注入优先（default 只是后备） |
| PreAction patch vs Necessary 推导的 parameters | PreAction patch 深度合并后的值优先（PreAction 在 Necessary 解析之后执行，优先级最高） |
| 多个 PreAction 修改同一字段 | 按执行顺序（数组顺序），后执行的覆盖先执行的 |

### 8.6 实际示例

以 Redis Sentinel 模式的 maxmemory 配置为例，追踪完整的值传递链：

```
1. 用户输入 Necessary:
   resource.redis.limits.memory = "4"  (4 Gi)

2. Baseline.parameters 引用:
   pod[0].resources.limits.memory = '"{{ .Necessary.resource.redis.limits.memory }}"'
   -> 解析为 "4"

3. Baseline.configurations[].values 传递到 redis-config:
   values:
     redis:
       resources:
         limits:
           memory: '"{{ .Necessary.resource.redis.limits.memory }}"'
   -> 解析为 { redis: { resources: { limits: { memory: "4" } } } }

4. redis-config Configuration 模板渲染:
   {{- define "redis.cal.memory" -}}
     ... 计算 80% 的内存 ...
   {{- end -}}
   
   {{- define "redis.maxMemory" -}}
     {{- if .Values.args.maxmemory }}          <- 检查用户是否自定义了 maxmemory
       {{- .Values.args.maxmemory }}           <- 优先使用自定义值
     {{- else }}
       {{- include "redis.cal.memory" (list .Values.redis.resources.limits.memory "max") }}
                                               <- 否则根据内存限制自动计算 80%
     {{- end }}
   {{- end -}}
   
   maxmemory {{ include "redis.maxMemory" . }}
   -> 最终值: maxmemory 3276mb  (4Gi * 80% ~= 3276 MiB)

5. 如果用户通过 parameters 修改了 args.maxmemory:
   args:
     maxmemory: "2gb"
   -> 最终值: maxmemory 2gb  (用户自定义值优先)
```

---

## 9. Secret 结构与标签

### 9.1 connection-credential 常见模式

现有中间件包通常会包含一个连接凭证 Configuration，在实例创建时生成对应的 Secret。这不是平台强制要求，而是一种推荐做法，各中间件可自行定义字段结构。格式如下：

```yaml
apiVersion: v1
kind: Secret
type: Opaque
metadata:
  name: <实例名>-credential
  namespace: <命名空间>
stringData:
  endpoint: <服务地址>
  port: "<服务端口>"
  username: <用户名>          # MinIO/MySQL 有，Redis 无
  password: "<密码>"
```

### 9.2 各中间件 Secret data 字段对照

| 中间件 | endpoint 格式 | port | username | password |
|--------|--------------|------|----------|----------|
| Redis | `<name>.<namespace>.svc` | 6379 | 无 | Redis requirepass |
| MinIO | `<name>-svc.<namespace>.svc` | 9000 | minio | MinIO SecretKey |
| MySQL | `<name>.<namespace>.svc` | 3306 | root | MySQL root password |

**以下是 Redis 包 connection-credential 模板中的实现细节，不是平台通用功能**：
- 支持 `encodePassword`（Base64 编码密码）：如果存在则优先使用 `b64dec` 解码后的值
- `redisServicePort`：如有自定义端口，可通过 values 覆盖默认的 `redis.port`

### 9.3 Labels 和 Annotations

#### 平台注入的 Labels

OpenSaola 自动设置的 `consts.Label*` 系列标签：
- `middleware.cn/component`：组件标识
- `middleware.cn/packageversion`：包版本
- `app.kubernetes.io/instance`：实例标识
- `app.kubernetes.io/name`：中间件名称

#### 各中间件自定义 Labels/Annotations 示例

**Baseline 级别 Labels**：
- `nephele/user: admin`：来源 Redis 基线
- `app: {{ .Globe.Name }}`：来源 MinIO 基线

**Baseline 级别 Annotations**：
- `baselineName`：基线展示名称
- `description`：基线描述
- `mode: "HA"`：高可用标记
- `active: "2"`：双活标记
- `datapath`：数据路径模板（来源 Redis）

**Operator Deployment Labels**：
- `app.kubernetes.io/name: <middleware>-operator`
- `app.kubernetes.io/instance: {{ .Globe.Name }}`

**实例 Pod Labels**（来自 parameters）：
- `app: {{ .Globe.Name }}`
- `middleware: redis`（来源 Redis）/ `operatorname: mysql-operator`（来源 MySQL）
- `harmonycloud.cn/statefulset: {{ .Globe.Name }}`

**实例 Pod Annotations**（来自 parameters）：
- `fixed-node-middleware-pod: "true"`：固定节点标记
- `fixed.ipam.harmonycloud.cn: "true"`：固定 IP 标记

---

## 10. Redis 完整适配案例

本章以 Redis 为例，完整展示一个中间件的适配细节。其他中间件（MinIO、MySQL）的适配模式类似，可参照本章结构理解。

### 10.1 Redis 包概述

**metadata.yaml 关键信息**：
- name: Redis
- version: 2.19.2-1.0.0
- app.version: 8.2.3, 8.0.4, 7.4.6, 7.2.11, 7.2.4（5 个版本）
- owner: HarmonyCloud
- type: db

**目录结构概要**：

| 目录 | 文件数 | 说明 |
|------|--------|------|
| baselines/ | 8 | 2 个 Operator 基线 + 6 个实例基线 |
| configurations/ | 14 | 连接凭证、配置、监控、服务等 |
| actions/ | 19 | 11 PreAction + 2 OpsAction + 6 NormalAction |
| crds/ | 2 | RedisCluster + RedisShake |
| manifests/ | 8 | 6 个 parameters + i18n + middlewareversionrule |

### 10.2 Redis Baselines 详解（8 个）

#### 10.2.1 Operator 基线（2 个）

**redis-operator-standard**（标准版 Operator）
- 文件：`baselines/operator-standard.yaml`
- GVK：`redis.middleware.hc.cn/v1alpha1/RedisCluster`
- Globe：`repository: 10.10.101.172:443`，`project: middleware`
- Deployment：replicas=1，资源 200m CPU / 512Mi Memory
- 镜像：`redis-operator:v2.19.1`
- preActions：pre-redis-operator-alertrule-labels、pre-redis-operator-affinity（soft）、pre-redis-operator-tolerations
- Configurations 引用：crd-redisclusters、crd-redisshakes、redis-operator-alertrule、redis-operator-dashboard、redis-operator-service、redis-operator-servicemonitor

**redis-operator-highly-available**（高可用版 Operator）
- 文件：`baselines/operator-highly-available.yaml`
- 与 standard 版的区别：**replicas=3**（高可用 3 副本）
- 其余配置完全一致（相同的 GVK、globe、权限、preActions、configurations）

#### 10.2.2 实例基线（6 个）

**redis-sentinel**（哨兵模式）
- 文件：`baselines/sentinel.yaml`
- metadata.name: `redis-sentinel`
- 引用 Operator: `redis-operator-standard`
- 部署类型（parameters.type）: `sentinel`
- 资源组件：Redis 节点 + Sentinel 节点（支持 Deployment 或 StatefulSet 部署方式）
- Necessary 定义：repository、version(默认8.2.3)、password、resource.redis(CPU/Memory/Replicas/Volume)、resource.sentinel(CPU/Memory/Replicas/DeployMethod)
- Parameters 核心字段：
  - `type: sentinel`
  - `sentinel.replicas/resources`：来自 `.Necessary.resource.sentinel.*`
  - `pod[0].middlewareImage`：复杂版本映射逻辑（根据 Redis 版本选择不同的镜像 tag）
  - `pod[0].resources`：来自 `.Necessary.resource.redis.*`
  - `replicas`：来自 `.Necessary.resource.redis.replicas`
  - `volumeClaimTemplates`：单卷 redis-data
- Configurations 引用（10 个）：redis-credential、redis-predixy-servicemonitor、redis-alertrule、redis-config、redis-sc、redis-servicemonitor、redis-shard-sc、redis-sentinel-configmap、redis-metrice-svc (注：代码中拼写如此)

**redis-sentinel-proxy**（哨兵代理模式）
- 文件：`baselines/sentinel-proxy.yaml`
- metadata.name: `redis-sentinel-proxy`
- 与 sentinel 基线的区别：
  - 额外的 `predixy` 组件配置（代理层）
  - Necessary 额外包含 `resource.predixy` 的 CPU/Memory 定义
  - Parameters 中增加 `predixy` 配置块（image、port 7617、replicas 3 等）
  - `replicas` 使用 `{{ div .Necessary.resource.redis.replicas 2 }}`（分片模式，副本数减半）
  - 额外引用 redis-predixy-configmap Configuration

**redis-sentinel-active**（哨兵双活模式）
- 文件：`baselines/sentinel-active.yaml`
- metadata.name: `redis-sentinel-active`
- annotations 中标记 `active: "2"`（双活标识）
- 与 sentinel 基线的区别：
  - `resource.redis.volumes`（多卷存储，替代单一 volume.storageClass）
  - `storageClassName` 使用 range 循环拼接多存储类
  - `storage` 通过 `index .Necessary.resource.redis.volumes 0 "size"` 获取

**redis-sentinel-proxy-active**（哨兵代理双活模式）
- 文件：`baselines/sentinel-proxy-active.yaml`
- metadata.name: `redis-sentinel-proxy-active`
- 组合了 sentinel-proxy 和 sentinel-active 的特性（双活 + 代理）
- annotations 中标记 `active: "2"`

**redis-cluster**（集群模式）
- 文件：`baselines/cluster.yaml`
- metadata.name: `redis-cluster`
- 部署类型（parameters.type）: `cluster`
- 与 sentinel 模式的区别：
  - 无 Sentinel 组件
  - Redis 节点默认 6 个（`replicas` 默认 6，最少 3）
  - 集群模式的 redis.conf 中 `cluster-enabled yes`
- Necessary 只包含 redis 资源（无 sentinel）

**redis-cluster-proxy**（集群代理模式）
- 文件：`baselines/cluster-proxy.yaml`
- metadata.name: `redis-cluster-proxy`
- 组合了 cluster 和 proxy 的特性
- 额外的 predixy 组件和配置
- 额外引用 redis-predixy-configmap

### 10.3 Redis Configurations 详解（14 个）

| 文件名 | metadata.name | 生成的 K8s 资源 | 关键 Values 引用 |
|--------|---------------|----------------|-----------------|
| connection-credential.yaml | redis-credential | Secret (Opaque) | `.Values.redis.port`、`.Values.redisPassword`、`.Values.encodePassword` |
| operator-alert.yaml | redis-operator-alertrule | PrometheusRule | `.Values.labels` |
| operator-dashboard-configmap.yaml | redis-operator-dashboard | ConfigMap (Grafana Dashboard) | 无 Values，纯 JSON 面板定义 |
| operator-service.yaml | redis-operator-service | Service (ClusterIP) | 无 Values，使用 `.Globe.Name/Labels` |
| operator-servicemonitor.yaml | redis-operator-servicemonitor | ServiceMonitor | 无 Values |
| proxy-servicemonitor.yaml | redis-predixy-servicemonitor | ServiceMonitor | `.Values.predixy.enableProxy` |
| redis-alert.yaml | redis-alertrule | PrometheusRule | `.Values.type`、`.Values.predixy.enableProxy`、`.Values.alertRules.*`、`.Values.clusterId`、`.Values.labels`、`.Values.customAlertRules` |
| redis-config.yaml | redis-config | ConfigMap | `.Values.type`、`.Values.version`、`.Values.redis.resources`、`.Values.args.*`（大量 Redis 配置参数）、`.Values.customVolumes`、`.Values.exporter` |
| redis-metrice-svc.yaml | redis-metrice-svc (注：代码中拼写如此) | Service (ClusterIP) | 无 Values |
| redis-predixy-configmap.yaml | redis-predixy-configmap | ConfigMap | `.Values.predixy.*`（enableProxy/port/workerThreads/args）、`.Values.redis.port/replicas`、`.Values.redisPassword`、`.Values.type`、`.Values.sentinel.port` |
| redis-sc.yaml | redis-sc | ConfigMap（StorageClass 映射） | `.Values.type`、`.Values.predixy.enableProxy`、`.Values.storageClassName` |
| redis-sentinel-configmap.yaml | redis-sentinel-configmap | ConfigMap | `.Values.type`、`.Values.sentinel.*`（port/replicas/args/auth）、`.Values.redis.port`、`.Values.redisPassword`、`.Values.encodePassword` |
| redis-servicemonitor.yaml | redis-servicemonitor | ServiceMonitor | 无 Values |
| redis-shard-sc.yaml | redis-shard-sc | ConfigMap（分片 StorageClass） | `.Values.type`、`.Values.predixy.enableProxy`、`.Values.storageClassName`、`.Values.redis.replicas` |

**redis-config.yaml 特殊说明**：
- 包含自定义函数 `redis.cal.memory`：将内存字符串（如 "4Gi"）转换为字节，计算 maxmemory（80%）和 output-buffer 限制
- 根据 `.Values.type` 条件输出 `cluster-enabled` 或 `slaveof` 配置
- 使用 `semverCompare` 进行版本特性判断（如 ACL 文件、ARM64-COW-BUG 修复）
- 大量 Redis 参数使用 `{{ .Values.args.xxx | default <默认值> }}` 模式

### 10.4 Redis Actions 详解（19 个）

**PreAction（11 个）**：

| 文件 | metadata.name | actionType | 功能说明 |
|------|---------------|------------|---------|
| pre-affinity.yaml | pre-redis-affinity | affinity | Redis 实例 Pod 反亲和性配置 |
| pre-alertrule-labels.yaml | pre-redis-alertrule-labels | alertlabel | 告警规则标签注入 |
| pre-hdpool.yaml | pre-redis-heimdallr-hdpool | hdpool | Heimdallr 网络池配置（IP 分配） |
| pre-hostnetwork.yaml | pre-redis-hostnetwork | hostnetwork | 主机网络模式配置 |
| pre-logging.yaml | pre-redis-logging | pre-logging | 日志配置（文件日志 + stdout） |
| pre-operator-affinity.yaml | pre-redis-operator-affinity | affinity | Operator Pod 反亲和性 |
| pre-operator-alertrule-labels.yaml | pre-redis-operator-alertrule-labels | alertlabel | Operator 告警规则标签 |
| pre-operator-tolerations.yaml | pre-redis-operator-tolerations | tolerations | Operator Pod 容忍度 |
| pre-proxy-affinity.yaml | pre-redis-proxy-affinity | affinity | Predixy 代理 Pod 反亲和性 |
| pre-sentinel-affinity.yaml | pre-redis-sentinel-affinity | affinity | Sentinel Pod 反亲和性 |
| pre-tolerations.yaml | pre-redis-tolerations | tolerations | Redis 实例 Pod 容忍度 |

**OpsAction（2 个）**：

| 文件 | metadata.name | actionType | 功能说明 |
|------|---------------|------------|---------|
| restart.yaml | redis-restart | restart | 中间件重启（检测是否有 predixy，分别处理） |
| migrate.yaml | redis-migrate | migrate | 节点迁移（检查主从角色，仅允许迁移 slave/sentinel/predixy） |

**NormalAction（6 个）**：

| 文件 | metadata.name | actionType | 功能说明 |
|------|---------------|------------|---------|
| datasecurity.yaml | redis-datasecurity | datasecurity | 数据安全（空步骤，标记 ignore） |
| expose-cluster-external.yaml | redis-cluster-expose-external | expose | 集群模式外部暴露（NodePort/Ingress） |
| expose-proxy.yaml | redis-proxy-expose | expose | 代理模式读写暴露 |
| expose-sentinel-external.yaml | redis-sentinel-expose-external | expose | 哨兵模式外部暴露 |
| expose-sentinel-readonly.yaml | redis-sentinel-expose-readonly | expose | 哨兵模式只读暴露 |
| expose-sentinel-readwrite.yaml | redis-sentinel-expose-readwrite | expose | 哨兵模式读写暴露 |

### 10.5 Redis CRD

Redis CRD 详情见 [6.1 各中间件自带 CRD](#61-各中间件自带-crd)。

RedisCluster CRD 的状态映射 annotations 将 Operator 的状态信息（phase、process、log）传递给 OpenSaola，实现统一的状态管理。

### 10.6 Redis Manifests

**parameters 文件（6 个）**：

| 文件 | 对应基线 | 说明 |
|------|---------|------|
| sentinelparameters.yaml | redis-sentinel | 哨兵模式运行时参数 |
| sentinelproxyparameters.yaml | redis-sentinel-proxy | 哨兵代理模式参数 |
| sentinelactiveparameters.yaml | redis-sentinel-active | 哨兵双活模式参数 |
| sentinelproxyactiveparameters.yaml | redis-sentinel-proxy-active | 哨兵代理双活参数 |
| clusterparameters.yaml | redis-cluster | 集群模式参数 |
| clusterproxyparameters.yaml | redis-cluster-proxy | 集群代理模式参数 |

每个 parameters 文件通过 `middlewareDefinition` 字段与对应的 MiddlewareBaseline 关联，定义了可在平台 UI 上动态调整的 Redis 配置参数（如 maxmemory-policy、maxclients、timeout 等），并标注每个参数是否需要重启生效。

**版本规则**：Redis 的 middlewareversionrule 定义了 6 个版本组（6.x、7.0、7.2、7.4、8.0、8.2），支持多级向上升级（如 6.x 可升到 8.2），但降级仅限同组内。

### 10.7 Redis 部署架构总结

Redis 包支持 6 种部署模式，覆盖从基础到企业级的各种场景：

| 部署模式 | 基线名称 | 特点 | 适用场景 |
|---------|---------|------|---------|
| 哨兵模式 | redis-sentinel | Redis + Sentinel，自动故障转移 | 标准高可用场景 |
| 哨兵代理模式 | redis-sentinel-proxy | 增加 Predixy 代理层，读写分离 | 需要读写分离和连接池管理 |
| 哨兵双活模式 | redis-sentinel-active | 多存储卷，双活部署 | 跨机房/跨可用区容灾 |
| 哨兵代理双活模式 | redis-sentinel-proxy-active | 双活 + 代理 | 跨机房容灾 + 读写分离 |
| 集群模式 | redis-cluster | Redis Cluster，数据自动分片 | 大数据量、高吞吐场景 |
| 集群代理模式 | redis-cluster-proxy | 集群 + Predixy 代理 | 集群模式 + 客户端透明代理 |

**选型建议**：
- 数据量小、可用性要求一般：sentinel（最简单）
- 需要读写分离：sentinel-proxy
- 跨机房容灾：sentinel-active 或 sentinel-proxy-active
- 数据量大、需要水平扩展：cluster
- 集群模式但需客户端兼容性：cluster-proxy

---

## 11. 与 OpenSaola 的交互接口

### 11.1 包如何被 OpenSaola 消费

1. **包上传**：通过 saola-cli 将中间件包打包为 Kubernetes Secret，推送到指定命名空间
2. **包发现**：OpenSaola 通过 Secret 的 labels 发现可用的中间件包
3. **包解析**：
   - 解压 Secret 中的 TAR 内容
   - 解析 metadata.yaml 获取包信息
   - 解析 baselines/ 获取所有可用的部署模式
   - 解析 configurations/、actions/、crds/、manifests/ 获取配套资源
4. **包注册**：将解析后的包信息注册到平台的中间件目录中

### 11.2 渲染流程中 operator 的角色

OpenSaola 在渲染流程中承担模板引擎的角色：

1. **接收创建请求**：用户选择基线并填写 Necessary 参数
2. **解析 Baseline**：
   - 加载对应的 MiddlewareBaseline
   - 加载关联的 MiddlewareOperatorBaseline（如果有）
3. **注入系统变量**：
   - 注入 `.Globe.Name`、`.Globe.Namespace` 等 6 个系统字段
   - 注入 `.Globe.repository`、`.Globe.project` 等 Operator globe 字段（仅 Operator 上下文）
   - 注入 `.Capabilities` 环境能力信息
4. **渲染 Parameters**：
   - 将 `{{ .Necessary.xxx }}` 替换为用户实际输入
   - 生成最终的 parameters 值
5. **执行 PreActions**：
   - 按顺序执行 preActions 列表中的每个 PreAction
   - PreAction 可以通过 KubectlEdit 修改 Middleware 资源的 parameters
6. **渲染 Configurations**：
   - 将 configurations[].values 中的模板引用替换为实际值
   - 将最终的 values 注入为 `.Values`
   - 使用 Go 模板引擎渲染每个 Configuration 的 template
7. **创建资源**：将渲染后的 Kubernetes 资源 apply 到集群

> **Parameters 到 CR 的映射**：
> - `Middleware.spec.parameters` 整体作为 CR 的 `spec` 字段发布
> - CR 继承 Middleware 的 labels、annotations、name、namespace
> - parameters 的 JSON 结构必须与目标 CRD 的 spec schema 完全对齐

### 11.3 状态同步

OpenSaola 通过以下机制同步中间件状态：

1. **CRD Status Watch**：监控中间件 CRD（如 RedisCluster、MysqlCluster）的 status 变化
2. **状态映射**：通过 CRD annotations 中的 JQ 表达式映射状态：
   - `harmonycloud-middleware-status-phase`：映射 phase 字段
   - `harmonycloud-middleware-status-process`：映射 process 字段
   - `harmonycloud-middleware-status-log`：映射 reason/log 字段
3. **Middleware Status 更新**：将映射后的状态写入 Middleware 资源的 status 中
4. **无 Operator 模式**（如 MinIO）：OpenSaola 直接监控 StatefulSet 的 status 来判断实例状态

---

## 12. 包开发最佳实践

1. **最小化原则**：只包含必需的文件，复杂功能按需添加。最简单的包只需 `metadata.yaml` + 1 个 baseline。

2. **命名一致性**：文件名与 `metadata.name` 保持一致，便于维护和排查。

3. **变量传递链路清晰化**：
   - `parameters` 中用 `.Necessary.*` 引用用户输入
   - `configurations[].values` 中用 `.Necessary.*` 和 `.Parameters.*` 桥接
   - Configuration 模板中只用 `.Values.*` 和 `.Globe.*`

4. **默认值策略**：
   - Necessary JSON 中设置合理的 `"default"` 值
   - Configuration 模板中用 `| default` 提供后备值
   - 关键参数使用 `"required": true` 强制用户填写

5. **版本兼容**：
   - 使用 `semverCompare` 处理版本差异
   - 将版本相关逻辑集中在 `pre-bigversion` PreAction 中
   - middlewareversionrule 中明确定义升降级路径

6. **能力检测**：
   - 使用 `{{ if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}` 检测可选功能
   - 避免在没有 Prometheus Operator 的集群上创建 PrometheusRule/ServiceMonitor

7. **安全考虑**：
   - 密码字段使用 `"type": "password"` 类型和正则校验
   - 不在 ConfigMap 中明文存储密码
   - 连接凭证统一使用 Secret 类型

8. **可观测性**：
   - 为每个中间件配置 ServiceMonitor、PrometheusRule 和 Grafana Dashboard
   - 告警规则使用可配置的阈值（通过 alertRules values 注入）
   - 支持自定义告警规则（customAlertRules）

9. **运维友好**：
   - 提供 restart Action 并处理有代理组件的场景
   - 节点迁移 Action 应校验角色限制（不允许迁移主节点）
   - PreAction 的 `exposed: true` 允许用户修改部署选项

10. **国际化**：
    - 所有面向用户的文本（baselineName、description、placeholder、label）在 i18n.yaml 中提供中英文对照
    - Action 的 alias 使用英文，在 i18n 中提供翻译
