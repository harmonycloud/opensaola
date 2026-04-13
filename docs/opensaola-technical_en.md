**English** | [中文](opensaola-technical.md)

# OpenSaola Technical Documentation

## Table of Contents

- [1. Architecture Overview](#1-architecture-overview)
  - [1.1 Design Philosophy](#11-design-philosophy)
  - [1.2 Overall Architecture Diagram (Text Description)](#12-overall-architecture-diagram-text-description)
  - [1.3 CRD Resource Relationships](#13-crd-resource-relationships)
  - [1.4 Interaction Boundaries with dataservice-baseline / saola-cli](#14-interaction-boundaries-with-dataservice-baseline--saola-cli)
- [2. CRD Complete Field Reference](#2-crd-complete-field-reference)
  - [2.1 Common Types](#21-common-types)
  - [2.2 Middleware](#22-middleware)
  - [2.3 MiddlewareBaseline](#23-middlewarebaseline)
  - [2.4 MiddlewareOperator](#24-middlewareoperator)
  - [2.5 MiddlewareOperatorBaseline](#25-middlewareoperatorbaseline)
  - [2.6 MiddlewarePackage](#26-middlewarepackage)
  - [2.7 MiddlewareAction](#27-middlewareaction)
  - [2.8 MiddlewareActionBaseline](#28-middlewareactionbaseline)
  - [2.9 MiddlewareConfiguration](#29-middlewareconfiguration)
- [3. State Machine and State Transitions](#3-state-machine-and-state-transitions)
  - [3.1 Phase/State Enum Values](#31-phasestate-enum-values)
  - [3.2 State Transition Diagrams](#32-state-transition-diagrams)
  - [3.3 Condition Types](#33-condition-types)
  - [3.4 State Change Triggers](#34-state-change-triggers)
- [4. Labels and Annotations Conventions](#4-labels-and-annotations-conventions)
- [5. Controller Reconcile Flow](#5-controller-reconcile-flow)
  - [5.1 Middleware Controller](#51-middleware-controller)
  - [5.2 MiddlewareBaseline Controller](#52-middlewarebaseline-controller)
  - [5.3 MiddlewareOperator Controller](#53-middlewareoperator-controller)
  - [5.4 MiddlewareOperatorBaseline Controller](#54-middlewareoperatorbaseline-controller)
  - [5.5 MiddlewarePackage Controller](#55-middlewarepackage-controller)
  - [5.6 MiddlewareAction Controller](#56-middlewareaction-controller)
  - [5.7 MiddlewareActionBaseline Controller](#57-middlewareactionbaseline-controller)
  - [5.8 MiddlewareConfiguration Controller](#58-middlewareconfiguration-controller)
- [6. Service Layer Details](#6-service-layer-details)
  - [6.1 Service Responsibilities and Core Methods](#61-service-responsibilities-and-core-methods)
- [7. Watcher / Syncer Mechanism](#7-watcher--syncer-mechanism)
  - [7.1 CR Watcher](#71-cr-watcherpkgservicewatchercustomresourcego)
  - [7.2 Synchronizer](#72-synchronizerpkgservicesynchronizersynchronizergo)
- [8. Upgrade Trigger Mechanism](#8-upgrade-trigger-mechanism)
  - [8.1 Trigger Methods](#81-trigger-methods)
  - [8.2 Upgrade Flow (Using Middleware as an Example)](#82-upgrade-flow-using-middleware-as-an-example)
  - [8.3 MiddlewareOperator Upgrade Special Handling](#83-middlewareoperator-upgrade-special-handling)
- [9. Deletion Flow and Finalizer](#9-deletion-flow-and-finalizer)
  - [9.1 Middleware Deletion](#91-middleware-deletion)
  - [9.2 MiddlewareOperator Deletion](#92-middlewareoperator-deletion)
  - [9.3 MiddlewarePackage Uninstallation](#93-middlewarepackage-uninstallation)
  - [9.4 Secret Deletion](#94-secret-deletion)
  - [9.5 Accidental CR Deletion](#95-accidental-cr-deletion)
  - [9.6 Finalizer](#96-finalizer)
- [10. Utility Packages (pkg/tools)](#10-utility-packages-pkgtools)
  - [10.1 Template Rendering (template.go)](#101-template-rendering-templatego)
  - [10.2 CUE Processing (cue.go)](#102-cue-processing-cuego)
  - [10.3 TAR Operations (tar.go)](#103-tar-operations-targo)
  - [10.4 JSON/Map Utilities (json.go)](#104-jsonmap-utilities-jsongo)
  - [10.5 Other Utilities](#105-other-utilities)
- [11. K8s Operations Wrapper (pkg/k8s)](#11-k8s-operations-wrapper-pkgk8s)
  - [11.1 Client Factory (kubeClient/client.go)](#111-client-factory-kubeclientclientgo)
  - [11.2 CRD Resource CRUD](#112-crd-resource-crud)
  - [11.3 Native Resource Operations](#113-native-resource-operations)
  - [11.4 Informer (informer.go)](#114-informerinformergo)
  - [11.5 Patch Utilities (patch.go)](#115-patch-utilities-patchgo)
  - [11.6 Pod Exec (k8s.go)](#116-pod-execk8sgo)
- [12. Key Design Decisions](#12-key-design-decisions)

---

## 1. Architecture Overview

### 1.1 Design Philosophy

OpenSaola is a middleware full-lifecycle management platform built on the Kubernetes Operator pattern. Its core design principles include:

- **Declarative Management**: Middleware desired state is declared through CRDs, and the Operator drives the actual state to converge toward the desired state
- **Package-Driven Publishing**: Middleware capabilities are packaged and distributed as Packages, which contain Baselines (baseline templates), Configurations (configuration templates), Actions (operation definitions), etc.
- **Templating and Merging**: Supports Go Template rendering and structured deep merging (StructMerge) to overlay baselines with user-defined parameters
- **CUE Orchestration**: Integrates the CUE language for field mapping and resource orchestration, supporting complex declarative operations
- **Multi-Layer Abstraction**: Baseline (cluster-level template) -> Operator/Middleware (namespace-level instance), achieving separation of concerns

API Group: `middleware.cn`, Version: `v1`

### 1.2 Overall Architecture Diagram (Text Description)

```
                     +-----------------+
                     |   saola-cli     |   (CLI tool: packaging, uploading packages to Secret)
                     +--------+--------+
                              |
                              v
                 +------------+------------+
                 |   K8s Secret (Package)  |   (zstd compression + tar archive)
                 +------------+------------+
                              |
                              v
              +---------------+---------------+
              |  MiddlewarePackage Controller  |  (watches Secrets, parses packages, publishes Baseline/Configuration etc.)
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
   | (CR instance) |      | + RBAC       |      | Exec Engine  |
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
   | Synchronizer |  (periodically syncs CR status to Middleware.Status.CustomResources)
   +-------------+
```

### 1.3 CRD Resource Relationships

```
MiddlewarePackage (Cluster)  -- owns -->  MiddlewareBaseline (Cluster)
                             -- owns -->  MiddlewareOperatorBaseline (Cluster)
                             -- owns -->  MiddlewareActionBaseline (Cluster)
                             -- owns -->  MiddlewareConfiguration (Cluster)

MiddlewareOperator (Namespaced) -- references --> MiddlewareOperatorBaseline
                                -- generates --> Deployment, ServiceAccount, Role/ClusterRole, RoleBinding/ClusterRoleBinding

Middleware (Namespaced) -- references --> MiddlewareBaseline
                        -- references --> MiddlewareOperatorBaseline (indirectly through the OperatorBaseline field)
                        -- generates --> CustomResource (CR managed by the Operator)
                        -- triggers --> MiddlewareAction (PreActions)

MiddlewareAction (Namespaced) -- references --> MiddlewareActionBaseline
```

### 1.4 Interaction Boundaries with dataservice-baseline / saola-cli

- **saola-cli**: Responsible for packaging middleware capabilities into tar + zstd format and uploading them to K8s Secrets (stored in `data_namespace`, which defaults to `default` in code but can be overridden via the `data_namespace` field in the `config.yaml` configuration file in actual deployments; saola-cli defaults to the `middleware-operator` namespace). Secrets must include the `middleware.cn/project: OpenSaola` label
- **dataservice-baseline**: Provides CUE/Go templates, baseline definitions, and Action definitions for 40+ middleware types. These contents are packaged and uploaded by saola-cli
- **OpenSaola**: Watches Secrets with the `middleware.cn/project: OpenSaola` label, automatically parses package contents, and publishes CRD resource instances (Baselines, Configurations, etc.)

---

## 2. CRD Complete Field Reference

### 2.1 Common Types

#### Permission

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| serviceAccountName | string | No | `""` | ServiceAccount name | Also used as the name for Role/ClusterRole and RoleBinding/ClusterRoleBinding |
| rules | []rbacv1.PolicyRule | No | nil | RBAC rule list | Standard K8s PolicyRule format |

#### OperatorBaseline (Associated Operator Template)

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| name | string | No | `""` | Name of the MiddlewareOperatorBaseline | Used to associate the Operator template |
| gvkName | string | No | `""` | GVK name, used to locate the CR type to publish | Must have a matching entry in OperatorBaseline.Spec.GVKs |

#### Configuration (Configuration Reference)

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| name | string | No | `""` | Name of the MiddlewareConfiguration | A corresponding Configuration must exist in the package |
| values | runtime.RawExtension | No | nil | Configuration values in JSON format | Supports Go Template rendering |

#### PreAction

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| name | string | No | `""` | Action name, corresponds to a MiddlewareActionBaseline name | Must be an ActionBaseline with BaselineType=PreAction |
| fixed | bool | No | false | Whether fixed (cannot be overridden) | When true, merging is skipped (see boundary notes below) |

> **fixed=true PreAction merge boundary notes**: A baseline PreAction with fixed=true does not accept user overrides; its parameters always use the values defined in the baseline. Same-named entries defined by the user in `Middleware.spec.preActions` will be ignored. Users cannot add PreActions not declared in the baseline.
| exposed | bool | No | false | Whether exposed | - |
| parameters | runtime.RawExtension | No | nil | PreAction parameters | JSON format |

#### Globe (Global Configuration)

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| repository | string | No | `""` | Image repository address | - |

#### State Enum

| Value | Description |
|-------|-------------|
| `Available` | Available |
| `Unavailable` | Unavailable |
| `Updating` | Updating |

#### Phase Enum

| Value | Description |
|-------|-------------|
| `""` (empty) | Unknown/Initial |
| `Checking` | Checking |
| `Checked` | Check completed |
| `Running` | Running |
| `Failed` | Failed |
| `UpdatingCustomResources` | Updating CRs |
| `BuildingRBAC` | Building RBAC |
| `BuildingDeployment` | Building Deployment |
| `Finished` | Finished |
| `MappingFields` | Mapping fields |
| `Executing` | Executing |

#### PermissionScope Enum

| Value | Description |
|-------|-------------|
| `Unknown` | Unknown |
| `Cluster` | Cluster-level (generates ClusterRole + ClusterRoleBinding) |
| `Namespace` | Namespace-level (generates Role + RoleBinding) |

#### ConfigurationType Enum

| Value | Description |
|-------|-------------|
| `""` (empty) | Unknown |
| `configmap` | ConfigMap |
| `serviceAccount` | ServiceAccount |
| `role` | Role |
| `roleBinding` | RoleBinding |
| `clusterRole` | ClusterRole |
| `clusterRoleBinding` | ClusterRoleBinding |
| `customResource` | Custom Resource |
| `customResourceBaseline` | Custom Resource Baseline |

#### BaselineType Enum

| Value | Description |
|-------|-------------|
| `PreAction` | Pre-action (executed before resource publishing) |
| `NormalAction` | Normal action (triggered manually by the user) |

> **About OpsAction**: `OpsAction` is not an explicitly defined `BaselineType` constant in the source code (the source only defines `WorkflowPreAction = "PreAction"` and `WorkflowNormalAction = "NormalAction"`), but this value is widely used in actual YAML files (e.g., `baselineType: "OpsAction"` in `restart.yaml`). The controller uses exclusion logic (`BaselineType != PreAction`) to determine whether to execute, so `OpsAction` behaves identically to `NormalAction` and works correctly.

#### Condition Type Constants

| Constant | Description |
|----------|-------------|
| `Checked` | Check condition |
| `BuildPreResource` | Build pre-resource |
| `BuildExtraResource` | Build extra resource |
| `ApplyRBAC` | Apply RBAC |
| `ApplyOperator` | Apply Operator (Deployment) |
| `ApplyCluster` | Apply CR |
| `MapCueFields` | Map CUE fields |
| `ExecuteAction` | Execute Action |
| `ExecuteCue` | Execute CUE |
| `ExecuteCmd` | Execute command |
| `ExecuteHttp` | Execute HTTP request |
| `Running` | Running |
| `TemplateParseWithBaseline` | Template parsing and baseline merging |
| `Updating` | Updating |

#### Condition Reason Constants Reference

> Source location: `OpenSaola/api/v1/common.go:120-147`

| Constant Name | Value | Corresponding CondType | Description |
|---------------|-------|----------------------|-------------|
| `CondReasonUnknown` | `"Unknown"` | General | Unknown state |
| `CondReasonIniting` | `"Initing"` | General | Default Reason during Condition initialization |
| `CondReasonCheckedFailed` | `"CheckedFailed"` | `Checked` | Check failed |
| `CondReasonCheckedSuccess` | `"CheckedSuccess"` | `Checked` | Check succeeded |
| `CondReasonBuildExtraResourceSuccess` | `"BuildExtraResourceSuccess"` | `BuildExtraResource` | Build extra resource succeeded |
| `CondReasonBuildExtraResourceFailed` | `"BuildExtraResourceFailed"` | `BuildExtraResource` | Build extra resource failed |
| `CondReasonApplyRBACSuccess` | `"ApplyRBACSuccess"` | `ApplyRBAC` | Apply RBAC succeeded |
| `CondReasonApplyRBACFailed` | `"ApplyRBACFailed"` | `ApplyRBAC` | Apply RBAC failed |
| `CondReasonApplyOperatorSuccess` | `"ApplyOperatorSuccess"` | `ApplyOperator` | Apply Operator (Deployment) succeeded |
| `CondReasonApplyOperatorFailed` | `"ApplyOperatorFailed"` | `ApplyOperator` | Apply Operator (Deployment) failed |
| `CondReasonApplyClusterSuccess` | `"ApplyClusterSuccess"` | `ApplyCluster` | Apply CR succeeded |
| `CondReasonApplyClusterFailed` | `"ApplyClusterFailed"` | `ApplyCluster` | Apply CR failed |
| `CondReasonMapCueFieldsSuccess` | `"MapCueFieldsSuccess"` | `MapCueFields` | Map CUE fields succeeded |
| `CondReasonMapCueFieldsFailed` | `"MapCueFieldsFailed"` | `MapCueFields` | Map CUE fields failed |
| `CondReasonExecuteActionSuccess` | `"ExecuteActionSuccess"` | `ExecuteAction` | Execute Action succeeded |
| `CondReasonExecuteActionFailed` | `"ExecuteActionFailed"` | `ExecuteAction` | Execute Action failed |
| `CondReasonExecuteCueSuccess` | `"ExecuteCueSuccess"` | `ExecuteCue` | Execute CUE succeeded |
| `CondReasonExecuteCueFailed` | `"ExecuteCueFailed"` | `ExecuteCue` | Execute CUE failed |
| `CondReasonExecuteCmdSuccess` | `"ExecuteCmdSuccess"` | `ExecuteCmd` | Execute command succeeded |
| `CondReasonExecuteCmdFailed` | `"ExecuteCmdFailed"` | `ExecuteCmd` | Execute command failed |
| `CondReasonRunningSuccess` | `"RunningSuccess"` | `Running` | Running succeeded |
| `CondReasonRunningFailed` | `"RunningFailed"` | `Running` | Running failed |
| `CondReasonUpdatingSuccess` | `"UpdatingSuccess"` | `Updating` | Upgrade succeeded |
| `CondReasonUpdatingFailed` | `"UpdatingFailed"` | `Updating` | Upgrade failed |
| `CondReasonTemplateParseWithBaselineSuccess` | `"TemplateParseWithBaselineSuccess"` | `TemplateParseWithBaseline` | Template parsing and baseline merging succeeded |
| `CondReasonTemplateParseWithBaselineFaild` | `"TemplateParseWithBaselineFaild"` | `TemplateParseWithBaseline` | Template parsing and baseline merging failed (**Note**: The source code contains a typo `Faild` instead of `Failed`; this exists in production code) |

---

### 2.2 Middleware

**Scope**: Namespaced  
**Short Name**: `mid`  
**Print Columns**: Type, Package, Baseline, Status, Age

#### 2.2.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| operatorBaseline | OperatorBaseline | No | zero value | Associated Operator baseline | If empty, the value from MiddlewareBaseline is used |
| baseline | string | No | `""` | Associated MiddlewareBaseline name | Must exist in the package |
| necessary | runtime.RawExtension | No | nil | Required parameters (e.g., image) | JSON format; compared with the baseline's necessary field, missing keys trigger an error (except repository) |
| preActions | []PreAction | No | nil | Pre-action list | Merged with baseline's preActions; entries with fixed=true are not merged |
| parameters | runtime.RawExtension | No | nil | Custom parameters | kubebuilder:pruning:PreserveUnknownFields; deep-merged with baseline |
| configurations | []Configuration | No | nil | Configuration list | Merged with baseline's configurations array |

**Interface Methods**:
- `GetConfigurations()` - Returns Spec.Configurations
- `GetUnified()` - Returns &Spec.Necessary
- `GetPreActions()` - Returns Spec.PreActions
- `GetMiddlewareName()` - Returns Name

#### 2.2.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | Observed Generation | Used to determine whether an update is needed |
| conditions | []metav1.Condition | No | nil | Condition list | patchMergeKey=type, patchStrategy=merge |
| customResources | CustomResources | No | zero value | Status snapshot of associated CRs | Periodically synced by the Synchronizer |
| state | State | No | `""` | Overall state | Set to Unavailable if any Condition is False |
| reason | string | No | `""` | State reason | Takes the Message from the first False Condition |

#### CustomResources Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| creationTimestamp | metav1.Time | CR creation time |
| phase | Phase | CR Phase/State |
| resources | v1.ResourceRequirements | Resource requirements |
| replicas | int | Replica count |
| reason | string | Reason |
| type | string | Type |
| include | Include | Associated resource list |
| disaster | *Disaster | Disaster recovery info (pointer, can be nil) |

#### Include Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| pods | []IncludeModel | Associated Pod list |
| pvcs | []IncludeModel | Associated PVC list |
| services | []IncludeModel | Associated Service list |
| statefulsets | []IncludeModel | Associated StatefulSet list |
| deployments | []IncludeModel | Associated Deployment list |
| daemonsets | []IncludeModel | Associated DaemonSet list |

#### IncludeModel Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| name | string | Resource name |
| type | string | Type (obtained from conditions) |
| source | string | Source label |
| sourceName | string | Source name label |

#### Disaster Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| gossip | *Gossip | Gossip disaster recovery info |
| data | *Data | Data synchronization info |

#### Gossip Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| advertiseAddress | string | Advertise address |
| advertisePort | int64 | Advertise port |
| phase | string | Gossip phase |
| clusterRole | string | Cluster role |
| clusterPhase | string | Cluster phase |
| role | string | Role |
| gossipPhase | string | Gossip phase |

#### Data Sub-structure

| Field | Type | JSON Tag | Description |
|-------|------|----------|-------------|
| phase | string | `phase` | Phase |
| address | string | `targetAddress` | Target address |
| oppositeAddress | string | `oppositeAddress` | Opposite-end address |
| oppositeClusterId | string | `oppositeClusterId` | Opposite-end cluster ID |
| oppositeClusterName | string | `oppositeClusterName` | Opposite-end cluster name |
| oppositeClusterNamespace | string | `oppositeClusterNamespace` | Opposite-end cluster namespace |

---

### 2.3 MiddlewareBaseline

**Scope**: Cluster  
**Short Name**: `mb`  
**Print Columns**: Type, Package, Status, Age

#### 2.3.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| operatorBaseline | OperatorBaseline | No | zero value | Associated Operator baseline | - |
| necessary | runtime.RawExtension | No | nil | Required parameter definitions | Defines the required parameters that Middleware must provide |
| preActions | []PreAction | No | nil | Pre-action definitions | - |
| parameters | runtime.RawExtension | No | nil | Default parameter template | kubebuilder:pruning:PreserveUnknownFields |
| configurations | []Configuration | No | nil | Default configuration list | - |

#### 2.3.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | Observed Generation | - |
| conditions | []metav1.Condition | No | nil | Condition list | - |
| state | State | No | `""` | State | - |

**Interface Methods**:
- `GetConfigurations()` - Returns Spec.Configurations
- `GetUnified()` - Returns nil (Baseline does not provide Unified/Necessary values)

---

### 2.4 MiddlewareOperator

**Scope**: Namespaced  
**Short Name**: `mo`  
**Print Columns**: Type, Package, Baseline, Status, Age

#### 2.4.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| baseline | string | Yes | `""` | Associated MiddlewareOperatorBaseline name | Checked=False is set during validation if empty |
| globe | *runtime.RawExtension | No | nil | Global variables (pointer) | Used for template rendering, equivalent to Middleware's necessary |
| preActions | []PreAction | No | nil | Pre-action list | - |
| permissionScope | PermissionScope | No | `""` | Permission scope | Determines whether Role or ClusterRole is generated |
| permissions | []Permission | No | nil | Permission list | Each Permission generates SA + Role/CR + RB/CRB |
| deployment | *runtime.RawExtension | No | nil | Deployment definition (pointer) | JSON-format appsv1.Deployment; skipped if nil or containers are empty |
| configurations | []Configuration | No | nil | Configuration list | - |

#### 2.4.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | Observed Generation | - |
| conditions | []metav1.Condition | No | nil | Condition list | - |
| state | State | No | `""` | State | - |
| operators | map[string]appsv1.DeploymentStatus | No | nil | Associated Deployment status | Key is the Deployment name |
| operatorAvailable | string | No | `""` | Availability (format "available/replicas") | e.g., "1/1" |
| reason | string | No | `""` | Reason | - |

**Interface Methods**:
- `GetConfigurations()` - Returns Spec.Configurations
- `GetUnified()` - Returns Spec.Globe
- `GetPreActions()` - Returns Spec.PreActions
- `GetMiddlewareName()` - Returns empty string

---

### 2.5 MiddlewareOperatorBaseline

**Scope**: Cluster  
**Short Name**: `mob`  
**Print Columns**: Type, Package, Status, Age

#### 2.5.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| gvks | []GVK | No | nil | Supported GVK list | Must not be empty during validation; each GVK's name/group/version/kind must not be empty |
| globe | *runtime.RawExtension | No | nil | Global variable template | - |
| preActions | []PreAction | No | nil | Pre-action definitions | - |
| permissionScope | PermissionScope | No | `""` | Permission scope | - |
| permissions | []Permission | No | nil | Permission definitions | - |
| deployment | *runtime.RawExtension | No | nil | Deployment template | - |
| configurations | []Configuration | No | nil | Configuration list | - |

#### GVK Sub-structure

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| name | string | Yes | `""` | GVK name identifier | Must not be empty during validation |
| group | string | Yes | `""` | API Group | Must not be empty during validation |
| version | string | Yes | `""` | API Version | Must not be empty during validation |
| kind | string | Yes | `""` | Kind | Must not be empty during validation |

#### 2.5.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | Observed Generation | - |
| conditions | []metav1.Condition | No | nil | Condition list | - |
| state | State | No | `""` | State | Unavailable if GVK validation fails |

**Interface Methods**:
- `GetConfigurations()` - Returns Spec.Configurations
- `GetUnified()` - Returns Spec.Globe

---

### 2.6 MiddlewarePackage

**Scope**: Cluster  
**Short Name**: `mp`  
**Print Columns**: Type, Age

#### 2.6.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| name | string | No | `""` | Package name | From metadata.yaml |
| catalog | Catalog | No | zero value | Package catalog | - |
| version | string | No | `""` | Version number | - |
| owner | string | No | `""` | Owner | - |
| type | string | No | `""` | Type | - |
| description | string | No | `""` | Description | - |

#### Catalog Sub-structure

| Field | Type | JSON Tag | Description |
|-------|------|----------|-------------|
| crds | []string | `crds` | CRD file name list |
| baselines | []string | `definitions` | Baseline file name list (note: JSON tag is definitions) |
| configurations | []string | `configurations` | Configuration file name list |
| actions | []string | `actions` | Action file name list |

#### 2.6.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | - | - |
| conditions | []metav1.Condition | No | nil | - | - |
| state | State | No | `""` | - | - |

---

### 2.7 MiddlewareAction

**Scope**: Namespaced  
**Short Name**: `ma`  
**Print Columns**: Status, Age, Reason

#### 2.7.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| baseline | string | Yes | - | Associated MiddlewareActionBaseline name | JSON tag has no omitempty |
| necessary | runtime.RawExtension | No | nil | Required parameters | - |
| middlewareName | string | Yes | - | Associated Middleware name | JSON tag has no omitempty |

#### 2.7.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | - | - |
| conditions | []metav1.Condition | No | nil | One Condition per Step (type is "STEP-{stepName}") | - |
| state | State | No | `""` | - | Reconcile is skipped if State is non-empty (one-time execution) |
| reason | metav1.StatusReason | No | `""` | Failure reason | Format is "conditionType,message" |

**Interface Methods**:
- `GetConfigurations()` - Returns nil
- `GetUnified()` - Returns &Spec.Necessary
- `GetPreActions()` - Returns empty slice
- `GetMiddlewareName()` - Returns Spec.MiddlewareName

---

### 2.8 MiddlewareActionBaseline

**Scope**: Cluster  
**Short Name**: `mab`  
**Print Columns**: Type, Package, Status, Age

#### 2.8.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| baselineType | BaselineType | Yes | - | Baseline type | PreAction or NormalAction |
| actionType | string | Yes | - | Action type | - |
| supportedBaselines | []string | No | nil | Supported baseline list | - |
| necessary | runtime.RawExtension | No | nil | Required parameter definitions | - |
| steps | []Step | Yes | - | Execution step list | Must not be empty during validation; each step.name must not be empty |

#### Step Sub-structure

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| name | string | Yes | - | Step name | Must not be empty during validation |
| type | string | No | `""` | Step type | `KubectlGet` / `KubectlExec` / `KubectlEdit` |
| output | Output | No | zero value | Output configuration | - |
| cue | string | No | `""` | CUE expression | Compile-validated |
| cmd | CMD | No | zero value | Command configuration | - |
| http | Http | No | zero value | HTTP request configuration | - |

#### Output Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| expose | bool | Whether to expose output to Context (for subsequent steps to reference) |
| type | string | Output type: `json` / `yaml` / `string` |

#### CMD Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| command | []string | Command argument list (executed via `sh -c`, joined into a single-line command) |

#### Http Sub-structure

| Field | Type | Description |
|-------|------|-------------|
| method | string | HTTP method |
| url | string | Request URL |
| header | map[string]string | Request headers |
| body | string | Request body |

#### 2.8.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | - | - |
| conditions | []metav1.Condition | No | nil | - | - |
| state | State | No | `""` | - | Unavailable if Steps validation fails |

---

### 2.9 MiddlewareConfiguration

**Scope**: Cluster  
**Short Name**: `mcf`  
**Print Columns**: Type, Package, Status, Age

#### 2.9.1 Spec Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| template | string | Yes | - | YAML template in Go Template format | kubebuilder:pruning:PreserveUnknownFields; after rendering, yaml.Unmarshal into unstructured.Unstructured |

#### 2.9.2 Status Fields

| Field | Type | Required | Default | Description | Boundary Conditions |
|-------|------|----------|---------|-------------|-------------------|
| observedGeneration | int64 | No | 0 | - | - |
| conditions | []metav1.Condition | No | nil | - | - |
| gvks | []metav1.GroupVersionKind | No | nil | Consumer GVK list | JSON tag is `gvks` |
| state | State | No | `""` | - | - |

---

## 3. State Machine and State Transitions

### 3.1 Phase/State Enum Values

**State** (used for Status.State across all CRDs):
- `Available` - All Conditions are True
- `Unavailable` - Unavailable (trigger conditions vary by CRD type, see below)
- `Updating` - In upgrade process

**Unavailable determination rule differences**:
- **Middleware / MiddlewareOperator**: Marked as Unavailable only when any Condition is **False** (Unknown does not trigger it)
- **MiddlewareAction**: Marked as Unavailable when any Condition is **False or Unknown**

**Phase** (used for CustomResources.Phase, synced from the underlying CR's status):
- `""`, `Checking`, `Checked`, `Running`, `Failed`, `UpdatingCustomResources`, `BuildingRBAC`, `BuildingDeployment`, `Finished`, `MappingFields`, `Executing`

### 3.2 State Transition Diagrams

#### Middleware/MiddlewareOperator State Transitions

```
Create/Update
    |
    v
[Check] -- failure --> Unavailable (Checked=False)
    |
    | success
    v
[Has LabelUpdate annotation?]
    |
    |-- yes --> [ReplacePackage] -- failure --> Unavailable (Updating=False)
    |              |
    |              | success
    |              v
    |          State=Updating --> State=Available (Updating=True)
    |
    |-- no --> [Compare Generation vs ObservedGeneration]
                   |
                   |-- generation == observedGeneration && not initialized || observedGeneration == 0
                   |       --> [HandleResource(Publish)]
                   |
                   |-- generation > observedGeneration || state == Updating
                   |       --> [HandleResource(Update)]
                   |
                   |-- other (initialized && generation == observedGeneration && state != Updating)
                   |       --> sync state only (defer computes State based on Conditions)
                   |       Note: this is the primary path during steady-state operation
                   
HandleResource flow:
    Checked=True?
        |-- no --> skip
        |-- yes -->
            [TemplateParseWithBaseline] -- failure --> TemplateParseWithBaseline=False
                |
                v
            [HandlePreActions] -- failure --> return error
                |
                v
            [HandleExtraResource] -- failure --> BuildExtraResource=False
                |
                v
            [BuildCustomResource / HandleRBAC + BuildDeployment] -- failure --> corresponding Condition=False
                |
                v
            All Conditions=True --> State=Available
```

#### MiddlewareAction State Transitions

```
Create
    |
    v
[State non-empty?] -- yes --> skip (one-time execution completed)
    |
    | no
    v
[Check] -- failure --> Unavailable
    |
    | success
    v
[Get ActionBaseline]
    |
    v
[BaselineType == PreAction?]
    |-- yes --> skip execution (PreActions are executed during Middleware/MO HandleResource)
    |-- no --> [Execute] execute Steps sequentially
                  |
                  | generates STEP-{name} Condition for each step
                  v
              All steps completed --> State=Available
              Any step failed --> State=Unavailable, Reason="type,message"
```

### 3.3 Condition Types

Each Condition contains the following fields:
- `Type`: Condition type (see the constants table in Section 2.1)
- `Status`: `True` / `False` / `Unknown`
- `Reason`: Corresponding CondReason constant
- `Message`: Detailed information ("success" on success, error message on failure)
- `LastTransitionTime`: Last transition time
- `ObservedGeneration`: Observed Generation

Condition initialization: Status=Unknown, Reason=Initing, Message="initializing"

### 3.4 State Change Triggers

| Trigger Event | Affected CRDs | Behavior |
|---------------|---------------|----------|
| Spec change (Generation increases) | All CRDs | Re-Reconcile |
| Status change | Middleware | **Ignored** (filtered by Predicate) |
| Adding `middleware.cn/update` annotation | Middleware, MiddlewareOperator | Triggers upgrade flow |
| Adding `middleware.cn/install` annotation to Secret | MiddlewarePackage | Triggers package installation |
| Adding `middleware.cn/uninstall` annotation to Secret | MiddlewarePackage | Triggers package uninstallation |
| Deployment change | MiddlewareOperator (Owns Deployment) | Syncs Deployment status and compares differences |
| Secret create/update/delete (with project label) | MiddlewarePackage (Watches Secret) | Creates/updates/deletes MiddlewarePackage |
| CR object deletion | Middleware (via Watcher) | Automatically rebuilds the CR |

---

## 4. Labels and Annotations Conventions

### Labels

| Key | Purpose | When Set | Example Value |
|-----|---------|----------|---------------|
| `middleware.cn/component` | Middleware component type | Set during Package publishing | `redis`, `mysql` |
| `middleware.cn/packageversion` | Package version number | Set during Package publishing | `1.0.0` |
| `middleware.cn/packagename` | Package name (Secret name) | Set during Package publishing | `redis-7.0.0-abc123` |
| `middleware.cn/project` | Owning project | Set during Secret creation | `OpenSaola` |
| `middleware.cn/app` | Application name | Set during Configuration publishing | `my-redis` |
| `middleware.cn/enabled` | Whether enabled | Set during install/uninstall | `true` / `false` |
| `middleware.cn/source` | Source identifier | During resource creation | - |
| `middleware.cn/sourcename` | Source name | During resource creation | - |
| `middleware.cn/definition` | Identifies the baseline name used by the CR (creation source) | Set by saola-cli during resource creation | `redis-master-slave` |

### Annotations

| Key | Purpose | When Set | Example Value |
|-----|---------|----------|---------------|
| `middleware.cn/update` | Upgrade target version | Set when user triggers an upgrade | `2.0.0` |
| `middleware.cn/baseline` | Upgrade target baseline name | Set when user triggers an upgrade | `redis-baseline-v2` |
| `middleware.cn/install` | Install marker | Set when user triggers installation | Presence triggers installation |
| `middleware.cn/uninstall` | Uninstall marker | Set when user triggers uninstallation | Presence triggers uninstallation |
| `middleware.cn/configurations` | Associated Configuration names | Set during Configuration publishing | `redis-configmap` |
| `middleware.cn/disasterSyncer` | Disaster recovery syncer GVK/Name | Configured by user | `group/version/kind/name` |
| `middleware.cn/dataSyncer` | Data syncer GVK/Name | Configured by user | `group/version/kind/name` |
| `middleware.cn/oppositeClusterId` | Opposite-end cluster ID | Configured by user | - |

---

## 5. Controller Reconcile Flow

### 5.1 Middleware Controller

**Watch Resource**: `v1.Middleware` (itself)  
**Predicate Filter**: Ignores Update events where only Status fields changed  
**Global Variable**: `MiddlewareInitializationCompleted` - Marks whether initialization is complete

> **MiddlewareInitializationCompleted boundary notes**:
> 1. Purpose: Ensures a full Publish for all existing Middleware instances when the Operator starts up (HandleResource(Publish) is executed even when generation == observedGeneration)
> 2. Only the first successfully executed Reconcile sets this flag to true; subsequent Reconciles enter the normal generation comparison logic
> 3. Newly created Middleware (observedGeneration == 0) is not affected by this flag, because the `observedGeneration == 0` condition is independent of this flag and always triggers a Publish

**Reconcile Main Logic**:

1. **Get Middleware**: Retrieved via `k8s.GetMiddleware`
   - If NotFound: Get cached copy from `MiddlewareCache`, call `HandleResource(Delete)` to delete associated resources, clear cache
   - If other error: Return error

2. **Deferred status update**:
   - Iterate all Conditions; if any Condition is False, State = Unavailable, Reason = that Condition's Message
   - Otherwise State = Available, Reason = ""
   - Call `k8s.UpdateMiddlewareStatus` to update status

3. **Check validation**: Call `middleware.Check`
   - Set Checked Condition to True

4. **ReplacePackage upgrade check**: Call `middleware.ReplacePackage`
   - Check whether `middleware.cn/update` annotation exists

5. **Generation comparison**:
   - `generation == observedGeneration && !initialized` or `observedGeneration == 0`: Call `HandleResource(Publish)`
   - `generation > observedGeneration` or `state == Updating`: Call `HandleResource(Update)`

**HandleResource Call Chain**:
```
HandleResource(Publish/Update/Delete)
    -> TemplateParseWithBaseline  (get Baseline -> deep-merge parameters/configurations/labels/annotations/PreActions -> template rendering)
    -> HandlePreActions           (iterate PreActions, execute PreAction-type ActionBaselines)
    -> handleExtraResource        (get and render MiddlewareConfigurations -> handle each one)
    -> buildCustomResource        (locate CR type via GVK -> create/update/delete CR -> start Watcher + Synchronizer)
```

### 5.2 MiddlewareBaseline Controller

**Watch Resource**: `v1.MiddlewareBaseline`  
**Predicate**: `GenerationChangedPredicate` (triggers only on Generation changes)

**Reconcile Main Logic**:
1. Get MiddlewareBaseline
2. Call `middlewarebaseline.Check` - Set Checked=True, State=Available, update status

### 5.3 MiddlewareOperator Controller

**Watch Resource**: `v1.MiddlewareOperator` (itself), `appsv1.Deployment` (Owns)  
**Global Variable**: `MiddlewareOperatorInitializationCompleted`

> **Note**: Unlike the Middleware Controller, MiddlewareOperator's `SetupWithManager` does **not** configure a Predicate to filter Status changes, so Status changes also trigger a Reconcile. The Middleware Controller uses a custom `predicate.Funcs` to filter out Update events where only Status changed, while the MiddlewareOperator Controller uses the default behavior.

**Reconcile Main Logic** (two phases):

**Phase 1 - handleMiddlewareOperator**:
1. Get MiddlewareOperator
   - NotFound: Get from cache -> HandleResource(Delete) -> clear cache
2. Deferred status update (same logic as Middleware)
3. Check validation: Verify baseline is not empty
4. ReplacePackage upgrade check
5. Generation comparison -> HandleResource(Publish/Update)

**Phase 2 - handleDeployment**:
1. Get MiddlewareOperator, check for `LabelUpdate` annotation (skip if updating)
2. Get Deployment
   - Success: Call `CompareDeployment` to compare actual vs expected differences, restore if inconsistent
   - Success: Associate to MO via OwnerReference, sync Deployment Status to MO.Status
   - NotFound but ApplyOperator=True: Re-run HandleResource(Publish) to rebuild

**HandleResource Call Chain**:
```
HandleResource(Publish/Update/Delete)
    -> TemplateParseWithBaseline  (get OperatorBaseline -> deep-merge Spec -> template-render entire Spec)
    -> HandlePreActions           (execute pre-actions)
    -> handleExtraResource        (publish extra resources - same as Middleware)
    -> handleRBAC                 (generate SA + Role/ClusterRole + RB/CRB)
    -> buildDeployment            (create/update/delete Deployment, set ControllerReference)
```

### 5.4 MiddlewareOperatorBaseline Controller

**Watch Resource**: `v1.MiddlewareOperatorBaseline`  
**Predicate**: `GenerationChangedPredicate`

**Reconcile Main Logic**:
1. Get MiddlewareOperatorBaseline
2. Call `middlewareoperatorbaseline.Check`
   - Validate GVKs is not empty
   - Validate each GVK's name/group/version/kind is not empty
   - Validation failure -> State=Unavailable, Checked=False

### 5.5 MiddlewarePackage Controller

**Watch Resource**: `v1.MiddlewarePackage` (itself), `corev1.Secret` (Watches)  
**Secret Filter**: Only processes Secrets with the `middleware.cn/project: OpenSaola` label

**Reconcile Main Logic**:

**HandlePackage**:
1. Get MiddlewarePackage
2. Call `middlewarepackage.Check` - Set Checked=True

**HandleSecret** (triggered by Secret Watch):
1. Get Secret
   - **Exists**:
     - Parse package -> create/update MiddlewarePackage
     - Has `install` annotation: Disable other enabled packages of the same component -> HandleResource(Publish) (publish Baseline/Config/Action) -> set enabled=true
     - Has `uninstall` annotation: HandleResource(Delete) -> set enabled=false
   - **Does not exist** (delete event):
     - Delete the corresponding MiddlewarePackage

**HandleResource(Publish) Call Chain**:
```
HandleResource(Publish)
    -> Get all MiddlewareBaselines from package -> Deploy each (set labels + OwnerReference -> Create)
    -> Get all MiddlewareOperatorBaselines from package -> Deploy each
    -> Get all MiddlewareActionBaselines from package -> Deploy each
    -> Get all MiddlewareConfigurations from package -> Deploy each
    (Each step rolls back already-published resources of the same type on failure)
```

### 5.6 MiddlewareAction Controller

**Watch Resource**: `v1.MiddlewareAction`  
**Predicate**: `GenerationChangedPredicate`

**Reconcile Main Logic**:
1. Get MiddlewareAction
2. **Non-empty State check**: If State is non-empty, return immediately (one-time execution semantics)
3. Deferred status update (similar to Middleware, but Unknown is also treated as Unavailable)
4. Check validation
5. Get ActionBaseline
6. If BaselineType != PreAction: Call `Execute` to run all Steps
   - Each Step executes CUE / CMD / HTTP based on type
   - Each Step generates an independent Condition (type "STEP-{name}")

### 5.7 MiddlewareActionBaseline Controller

**Watch Resource**: `v1.MiddlewareActionBaseline`  
**Predicate**: `GenerationChangedPredicate`

**Reconcile Main Logic**:
1. Get MiddlewareActionBaseline
2. Call `middlewareactionbaseline.Check`
   - Validate Steps is not empty
   - Validate each Step's name is not empty
   - Optional compile validation for CUE steps

### 5.8 MiddlewareConfiguration Controller

**Watch Resource**: `v1.MiddlewareConfiguration`  
**Predicate**: `GenerationChangedPredicate`

**Reconcile Main Logic**:
1. Get MiddlewareConfiguration
2. Call `middlewareconfiguration.Check` - Set Checked=True, State=Available

---

## 6. Service Layer Details

### 6.1 Service Responsibilities and Core Methods

#### middleware (pkg/service/middleware/middleware.go)

| Method | Responsibility |
|--------|---------------|
| `Check` | Validate Middleware, set Checked Condition |
| `ReplacePackage` | Handle upgrade flow: get new package -> get new Baseline -> update Spec/Labels -> wait for package readiness |
| `TemplateParseWithBaseline` | Get Baseline -> deep-merge parameters/configurations/labels/annotations/PreActions -> template-render Parameters, Configurations, ObjectMeta |
| `HandleResource` | Orchestrator: template parsing -> PreActions -> extra resources -> CR |
| `handleExtraResource` | Get and render MiddlewareConfigurations -> handle each (publish in order, delete in reverse order) |
| `buildCustomResource` | Locate CR type via GVK -> create/update CR -> start Watcher + Synchronizer |

**NecessaryIgnore**: `["repository"]` - Key list to ignore during required parameter validation

#### middlewarebaseline (pkg/service/middlewarebaseline/)

| Method | Responsibility |
|--------|---------------|
| `Check` | Validate and update status |
| `Get` | Get from cluster, or get from package and cache (`sync.Map`) |
| `Deploy` | Set labels + OwnerReference -> create resource |

#### middlewareoperator (pkg/service/middlewareoperator/)

| Method | Responsibility |
|--------|---------------|
| `Check` | Validate baseline is not empty |
| `ReplacePackage` | Handle upgrade (similar to Middleware) |
| `TemplateParseWithBaseline` | Get OperatorBaseline -> merge entire Spec -> render -> HandlePreActions |
| `HandleResource` | Orchestrator: template parsing -> PreActions -> extra resources -> RBAC -> Deployment |
| `handleRBAC` | Iterate permissions -> generate SA + Role/ClusterRole + RoleBinding/ClusterRoleBinding |
| `buildDeployment` | Deserialize Deployment -> set name/namespace/labels/ownerRef -> create/update/delete |
| `CompareDeployment` | Compare actual Deployment with expected, restore if inconsistent |

#### middlewarepackage (pkg/service/middlewarepackage/)

| Method | Responsibility |
|--------|---------------|
| `Check` | Validate and update status |
| `HandleSecret` | Handle Secret events: create MiddlewarePackage -> execute install/uninstall based on annotations |
| `HandleResource` | Get all Baselines/Configurations/Actions from package -> batch Deploy/Delete (rollback on failure) |

#### middlewareaction (pkg/service/middlewareaction/)

| Method | Responsibility |
|--------|---------------|
| `Check` | Validate |
| `Execute` | Get ActionBaseline -> execute CUE/CMD/HTTP steps sequentially |
| `HandlePreActions` | Iterate pre-actions -> get ActionBaseline (must be PreAction type) -> ExecutePreAction |
| `ExecutePreAction` | Execute CUE step mapping and apply (modifies the passed-in unstructured object) |
| `executeCue` | Execute CUE steps (KubectlGet/KubectlExec/KubectlEdit) |
| `executeCmd` | Execute CMD steps (runs commands via `sh -c`) |
| `executeHTTP` | Execute HTTP steps (supports custom method/url/header/body) |
| `TemplateParseWithBaseline` | Template-render the CUE/CMD/HTTP fields of Steps |

**Step Type Constants**:
- `KubectlGet` - Get K8s resource
- `KubectlExec` - Execute command in a Pod container
- `KubectlEdit` - Edit/create K8s resource

#### middlewareconfiguration (pkg/service/middlewareconfiguration/)

| Method | Responsibility |
|--------|---------------|
| `Check` | Validate and update status |
| `Get` | Get from cluster, or get from package and cache |
| `GetTemplateParsedMiddlewareConfigurations` | Get and render all Configuration templates |
| `Handle` | Parse rendered template into unstructured -> determine if Namespaced -> set labels/annotations/ownerRef -> create/update/delete |
| `Deploy` | Set labels + OwnerReference -> create resource |

#### packages (pkg/service/packages/)

| Method | Responsibility |
|--------|---------------|
| `Get` | Read from Secret -> zstd decompress -> tar parse -> read metadata.yaml |
| `List` | List all packages (filter Secrets by labels) |
| `GetMetadata` | Get package metadata |
| `GetConfigurations` | Get all MiddlewareConfigurations from package |
| `GetMiddlewareBaselines` | Get all MiddlewareBaselines from package |
| `GetMiddlewareOperatorBaselines` | Get all MiddlewareOperatorBaselines from package |
| `GetMiddlewareActionBaselines` | Get all MiddlewareActionBaselines from package |
| `Compress` | zstd compression |
| `DeCompress` | zstd decompression |

**Package Format**: Secret.Data["package"] -> zstd compressed -> tar archive
- `metadata.yaml` - Package metadata
- `crds/` - CRD definition files
- `baselines/` - Baseline YAML files
- `configurations/` - Configuration YAML files
- `actions/` - Action YAML files

#### customresource (pkg/service/customresource/)

| Method | Responsibility |
|--------|---------------|
| `HandleGvk` | Get GVK from Middleware -> Baseline -> OperatorBaseline |
| `GetNeedPublishCustomResource` | Build the CR (unstructured) to be published |
| `RestoreIfIllegalUpdate` | Check for kubectl modifications and revert (determined via managedFields) |

#### status (pkg/service/status/condition.go)

| Method | Responsibility |
|--------|---------------|
| `GetCondition` | Get a Condition of the specified type from the Conditions list; initialize if not present |
| `Condition.Failed` | Set Condition to False + corresponding failure Reason |
| `Condition.Success` | Set Condition to True + corresponding success Reason, Message="success" |
| `Condition.SuccessWithMsg` | Set Condition to True + custom Message |
| `ConditionInit` | Create initial Condition (Status=Unknown, Reason=Initing) |

#### consts (pkg/service/consts/)

Defines all constants:
- `HandleAction`: `publish` / `delete` / `update`
- Label and Annotation key constants
- Error variables: `SameTypeMiddlewareExists`, `SameTypeMiddlewareOperatorExists`

---

## 7. Watcher / Syncer Mechanism

### 7.1 CR Watcher (pkg/service/watcher/customresource.go)

**CustomResourceWatcher Structure**:
- `GVK` - Resource type being watched
- `Namespace` - Namespace
- `StopChan` - Stop channel
- `Counter` - Reference count (number of Middleware instances with the same GVK+Namespace)

**Workflow**:

1. **At startup**: `StartCRWatcher` is started as a goroutine in main.go
   - Wait for cache sync
   - List all Middleware instances
   - For each Middleware, resolve GVK -> start Informer + Synchronizer
   - Reuse Watcher for the same GVK+Namespace (Counter++)

2. **At runtime**: Started in `buildCustomResource` each time a Middleware publishes a CR
   - If Watcher does not exist: Create new Watcher -> start Informer
   - If Watcher already exists: Calibrate Counter

3. **Event Handling** (`NewResourceEventHandlerFuncs`):
   - **AddFunc**: Log
   - **UpdateFunc**: Log, compare ResourceVersion
   - **DeleteFunc**: Check whether the OwnerReference's Middleware exists
     - Does not exist: Close Watcher + Synchronizer
     - Exists: Automatically rebuild the CR (clear ResourceVersion and Create)

4. **Shutdown**: `CloseCRWatcher`
   - Counter == 1: close(StopChan) -> remove from Map
   - Counter > 1: Counter--

### 7.2 Synchronizer (pkg/service/synchronizer/synchronizer.go)

**Function**: Periodically syncs CR status to Middleware.Status.CustomResources every second

**Sync Contents**:
1. Get CR status -> deserialize into CustomResources
2. Extract Phase (from `phase` or `state` field)
3. Extract Replicas (from `replicas` or `size` field)
4. Extract Reason
5. Collect associated resources (via OwnerReference and Label matching):
   - StatefulSet, Deployment, DaemonSet, Pod, Service, PVC
6. Sync disaster recovery info (if `disasterSyncer` / `dataSyncer` annotations exist)
7. Process Conditions (extracted from CR status using multiple keys: conditions, sentinelConditions, proxyConditions, etc.)
8. Sort all Include lists (by Name)
9. Compare using DeepEqual to detect changes; update only when changes are found

**Label Matching Logic**: Uses the `GeneralLabelKeys` list to match associated resources:
- `cluster-name`, `app`, `middleware.cn/app`, `esname`, `middleware_expose_middlewarename`, `app.kubernetes.io/instance`

**Stop Mechanism**: Managed via `SyncCustomResourceStopChanMap`; the corresponding stop channel is closed when the CR is deleted

---

## 8. Upgrade Trigger Mechanism

### 8.1 Trigger Methods

Add the following to the **Annotations** of a Middleware or MiddlewareOperator:
- `middleware.cn/update: <target version>` - Triggers the upgrade
- `middleware.cn/baseline: <target baseline name>` - Specifies the post-upgrade baseline (Middleware only)

### 8.2 Upgrade Flow (Using Middleware as an Example)

```
LabelUpdate annotation detected
    |
    v
Acquire lock (updatingLocker sync.Mutex)
    |
    v
Re-confirm annotation exists (prevent concurrency issues)
    |
    v
Set State=Updating -> update status
    |
    v
Checked=True?
    |-- no --> return
    |-- yes -->
        Get new package (find MiddlewarePackage by component + version labels)
            |
            v
        New package count != 1 --> error
            |
            v
        Get target Baseline from new package
            |
            v
        Update Middleware:
            - Spec.Baseline = new Baseline name
            - Labels[packageversion] = new version
            - Labels[packagename] = new package name
            - Remove update annotation
            |
            v
        Wait for package readiness (max 3 retries, 5 seconds each)
            |
            v
        Update Middleware resource
            |
            v
        Set Updating Condition
        defer: success -> Updating=True / failure -> Updating=False
```

### 8.3 MiddlewareOperator Upgrade Special Handling

- During upgrade, `Globe` and `PreActions` are preserved; all other Spec fields are reset
- Templates are re-fetched from the new Baseline

---

## 9. Deletion Flow and Finalizer

### 9.1 Middleware Deletion

When a Middleware is deleted (NotFound detected during Reconcile):
1. Get cached copy from `MiddlewareCache`
2. Call `HandleResource(Delete)`:
   - Delete extra resources created by MiddlewareConfigurations in reverse order
   - Delete the CustomResource
3. Clear cache

### 9.2 MiddlewareOperator Deletion

When a MiddlewareOperator is deleted:
1. Get cached copy from `MiddlewareOperatorCache`
2. Call `HandleResource(Delete)`:
   - Delete extra resources in reverse order
   - Delete RBAC resources (SA, Role/ClusterRole, RB/CRB)
   - Delete Deployment
3. Clear cache

### 9.3 MiddlewarePackage Uninstallation

Triggered via the `middleware.cn/uninstall` annotation on the Secret:
1. Call `HandleResource(Delete)`
   - Delete all MiddlewareBaselines
   - Delete all MiddlewareOperatorBaselines
   - Delete all MiddlewareActionBaselines
   - Delete all MiddlewareConfigurations
2. Set `enabled=false`, remove `uninstall` annotation

### 9.4 Secret Deletion

When a Secret is deleted:
1. Delete the corresponding MiddlewarePackage

### 9.5 Accidental CR Deletion

When a CR watched by a Watcher is deleted:
1. Check whether the OwnerReference's Middleware exists
2. If it exists: Automatically rebuild the CR (clear ResourceVersion and re-Create)
3. If it does not exist: Close the Watcher and Synchronizer

### 9.6 Finalizer

The current codebase **does not use Finalizers**. Deletion handling relies on:
- NotFound detection in Reconcile + Cache mechanism
- K8s OwnerReference cascading deletion (ControllerReference)
- Watcher DeleteFunc event handling

---

## 10. Utility Packages (pkg/tools)

### 10.1 Template Rendering (template.go)

#### Quoter Interface

All CRDs that require template rendering implement this interface:

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

#### TemplateValues (Template Variables)

| Field | Source | Description |
|-------|--------|-------------|
| `.Globe` | Quoter.GetUnified() + required fields | Global variables (Name, Namespace, Labels, Annotations, PackageName, MiddlewareName) |
| `.Necessary` | Quoter.GetUnified() | Required parameters |
| `.Values` | Configuration.Values | Configuration values |
| `.Step` | Context("step") | Step output (for data passing between Action steps) |
| `.Capabilities.KubeVersion` | K8s ServerVersion | K8s version info (Version, Major, Minor, GitVersion) |
| `.Parameters` | Rendered Parameters | Parameters (set during TemplateParseWithBaseline) |

#### Template Functions (funcs.go)

Based on the Sprig function library, with additional functions:
- `toToml` - Convert to TOML
- `toYaml` / `fromYaml` / `fromYamlArray` - YAML conversion
- `toJson` / `fromJson` / `fromJsonArray` - JSON conversion
- `toCue` - Convert to CUE format (supports recursive conversion of map/slice/string/bool/int/float/null)

Security-sensitive functions removed: `env`, `expandenv`

#### YAML Go Template Processing (yaml.go - ProcessYAMLGoTemp)

Handles the issue of Go Template syntax being wrapped in quotes after YAML serialization:
- `key: '{{...}}'` -> `key: {{...}}` (remove single quotes)
- `key: "{{...}}"` -> `key: {{...}}` (remove double quotes)
- `key: '"{{...}}"'` -> `key: '{{...}}'` (remove outer single quotes and inner double quotes)
- `key: "'{{...}}'"` -> `key: "{{...}}"` (remove outer double quotes and inner single quotes)

### 10.2 CUE Processing (cue.go)

`ParseAndAddCueFile(bi, fieldName, content)` - Parse CUE content and add to build.Instance

### 10.3 TAR Operations (tar.go)

```go
type TarInfo struct {
    Name  string
    Files map[string][]byte  // key: file path (top-level directory stripped), value: file content
}
```

- `ReadTarInfo(data)` - Read all files in a tar archive
- `TarInfo.ReadFile(name)` - Find file by name (fuzzy matching using strings.Contains)

### 10.4 JSON/Map Utilities (json.go)

#### StructMerge (Deep Merge)

Supports two modes:
- `StructMergeMapType` - Map deep merge
- `StructMergeArrayType` - Array merge

**Map Merge Rules**:
- Recursively merge nested maps
- Recursively merge nested arrays
- Other types: new overwrites old

**Array Merge Rules**:
- If elements are maps: Match by `ArrayStructKey` (`["name", "serviceAccountName"]`)
  - Matching element found: Recursively merge
  - No matching element found: Append
  - No structured key: Merge by index
- Other types: Directly replace old with new

#### CompareJson

Compares only keys that exist in old; supports sorting struct arrays by name before comparison

#### ExtractJsonKey

Flattens nested JSON into a flat map with `key.subkey.index` format

### 10.5 Other Utilities

- **gvk.go**: GVK <-> String conversion (format `group/version/kind`)
- **array.go**: `IsExistInStringSlice` - String slice search
- **map.go**: `JsonToMap` - JSON bytes -> map

---

## 11. K8s Operations Wrapper (pkg/k8s)

### 11.1 Client Factory (kubeClient/client.go)

| Method | Return Type | Purpose |
|--------|------------|---------|
| `GetDynClient` | `*dynamic.DynamicClient` | Dynamic client (used by Informer) |
| `GetRuntimeClient` | `client.Client` | controller-runtime client |
| `GetApiextensionsv1Client` | `*apiextclientset.Clientset` | CRD operations client |
| `GetClientSet` | `*kubernetes.Clientset` | Standard clientset (used for Pod exec) |
| `GetDiscoveryClient` | `*discovery.DiscoveryClient` | Resource discovery client |

### 11.2 CRD Resource CRUD

Each CRD resource has a corresponding k8s operations file providing a unified CRUD interface:

| Resource | File | Scope | Cache | Status Update |
|----------|------|-------|-------|---------------|
| Middleware | middleware.go | Namespaced | `MiddlewareCache` (sync.Map) | RetryOnConflict + DeepEqual skip |
| MiddlewareOperator | middlewareoperator.go | Namespaced | `MiddlewareOperatorCache` | RetryOnConflict + DeepEqual skip |
| MiddlewareBaseline | middlewarebaseline.go | Cluster | None | RetryOnConflict |
| MiddlewareOperatorBaseline | middlewareoperatorbaseline.go | Cluster | None | RetryOnConflict |
| MiddlewarePackage | middlewarepackage.go | Cluster | `MiddlewarePackageCache` | RetryOnConflict |
| MiddlewareAction | middlewareaction.go | Namespaced | `MiddlewareActionCache` | RetryOnConflict |
| MiddlewareActionBaseline | middlewareactionbaseline.go | Cluster | None | RetryOnConflict |
| MiddlewareConfiguration | middlewareconfiguration.go | Cluster | None | RetryOnConflict |

### 11.3 Native Resource Operations

| File | Resource | Operations |
|------|----------|-----------|
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

### 11.4 Informer (informer.go)

`NewInformerOptUnit` - Create and run an Informer:
- Uses DynamicClient's ListWatch
- 30-second resync
- Custom Indexer: apiVersion, kind
- Automatic panic recovery and restart
- Converts GVK to GVR via `GetGroupVersionResource`

### 11.5 Patch Utilities (patch.go)

Supports three Patch types:
- `JSONPatchType` - JSON Patch
- `MergePatchType` - JSON Merge Patch
- `StrategicMergePatchType` - Strategic Merge Patch

Provides `CreatePatch`, `ApplyPatch`, `ValidatePatch` methods

### 11.6 Pod Exec (k8s.go)

`ExecCommandInContainer` - Execute commands in a Pod container (via SPDY Executor)

---

## 12. Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| Use sync.Map to cache Baseline/Configuration/OperatorBaseline/ActionBaseline | Avoid frequent decompression reads from Secrets; paired with periodic cleanup (cache_cleanup_interval, default 1800 seconds) |
| Get object from Cache when Middleware is deleted | Deleted objects cannot be retrieved from the API Server; cache is needed for resource cleanup |
| Middleware Controller uses Predicate to filter Status updates | Prevents Status updates from triggering infinite Reconcile loops |
| One-time execution semantics for MiddlewareAction (skip if State is non-empty) | Actions are operations, not declarative state; they should not be re-executed after completion |
| Automatically rebuild CR when deleted | Protects middleware instances from accidental deletion; implemented via Watcher DeleteFunc |
| MiddlewareOperator compares Deployment differences and restores | Prevents Deployments from being externally modified (e.g., kubectl edit); ensures consistency with the declared Spec |
| Use sync.Mutex locking during upgrades | Prevents race conditions from concurrent upgrades |
| Disable other enabled packages of the same component during MiddlewarePackage installation | Ensures only one active version per middleware type |
| Delete Configuration resources in reverse order | Avoids dependency issues (earlier resources may be depended on by later ones) |
| Roll back already-published resources on HandleResource Publish failure | Maintains transactional consistency, prevents partial resource remnants |
| Use Go Template + Sprig + custom functions | Compatible with Helm template syntax, reducing learning curve |
| Use CUE language for field mapping and resource orchestration | CUE provides stronger type constraints and data validation capabilities |
| Secret as package storage medium | Leverages K8s native storage without additional storage components; zstd compression reduces size |
| OwnerReference cascading deletion | Leverages K8s GC for automatic child resource cleanup, reducing manual cleanup code |
| Status updates use RetryOnConflict | Handles concurrent update conflicts, ensures eventual consistency |
| Middleware/MO status updates use DeepEqual skip | Reduces unnecessary API calls |
| MiddlewareAction periodic cleanup (checked every 600 seconds) | Cleans up expired Actions older than cache_cleanup_interval |
