**English** | [中文](opensaola-packaging.md)

# Dataservice-Baseline Package Adaptation Technical Documentation

## Table of Contents

- [1. Architecture Overview](#1-architecture-overview)
  - [1.1 Purpose and Role of dataservice-baseline](#11-purpose-and-role-of-dataservice-baseline)
  - [1.2 Middleware Package Directory Structure Convention](#12-middleware-package-directory-structure-convention)
  - [1.3 Component Relationship Diagram](#13-component-relationship-diagram)
  - [1.4 Interaction Flow with OpenSaola](#14-interaction-flow-with-opensaola)
- [2. metadata.yaml Specification](#2-metadatayaml-specification)
  - [2.1 Complete Field Description](#21-complete-field-description)
- [3. Baselines System](#3-baselines-system)
  - [3.1 Definition and Purpose of Baselines](#31-definition-and-purpose-of-baselines)
  - [3.2 Baseline File Format Details](#32-baseline-file-format-details)
  - [3.3 Baseline Value Merging Logic](#33-baseline-value-merging-logic)
- [4. Configurations System](#4-configurations-system)
  - [4.1 Configuration File Format and Loading Mechanism](#41-configuration-file-format-and-loading-mechanism)
  - [4.2 Configuration Type Classification](#42-configuration-type-classification)
  - [4.3 Go Template Syntax Usage](#43-go-template-syntax-usage)
  - [4.4 Template Variable Sources and Rendering Flow](#44-template-variable-sources-and-rendering-flow)
- [5. Actions System](#5-actions-system)
  - [5.1 Action Definition Format and Field Description](#51-action-definition-format-and-field-description)
  - [5.2 Action Type Classification](#52-action-type-classification)
  - [5.3 Action Execution Mechanism](#53-action-execution-mechanism)
- [6. CRD Definitions](#6-crd-definitions)
  - [6.1 CRDs Bundled with Each Middleware](#61-crds-bundled-with-each-middleware)
  - [6.2 Relationship with OpenSaola CRDs](#62-relationship-with-opensaola-crds)
- [7. Manifests Directory](#7-manifests-directory)
  - [7.1 middlewareversionrule Format and Usage](#71-middlewareversionrule-format-and-usage)
  - [7.2 parameters File (Form Parameter Definitions)](#72-parameters-file-form-parameter-definitions)
  - [7.3 i18n Internationalization](#73-i18n-internationalization)
  - [7.4 hiddenmenus Configuration](#74-hiddenmenus-configuration)
- [8. Rendering Override Priority](#8-rendering-override-priority)
  - [8.1 Value Sources at Each Level](#81-value-sources-at-each-level)
  - [8.2 Override Priority Chain (Low to High)](#82-override-priority-chain-low-to-high)
  - [8.3 Merge Strategy (Deep Merge vs Replace)](#83-merge-strategy-deep-merge-vs-replace)
  - [8.4 Template Variable Resolution Order](#84-template-variable-resolution-order)
  - [8.5 Conflict Resolution Strategy](#85-conflict-resolution-strategy)
  - [8.6 Practical Example](#86-practical-example)
- [9. Secret Structure and Labels](#9-secret-structure-and-labels)
  - [9.1 connection-credential Common Patterns](#91-connection-credential-common-patterns)
  - [9.2 Secret Data Field Comparison Across Middleware](#92-secret-data-field-comparison-across-middleware)
  - [9.3 Labels and Annotations](#93-labels-and-annotations)
- [10. Redis Complete Adaptation Case Study](#10-redis-complete-adaptation-case-study)
  - [10.1 Redis Package Overview](#101-redis-package-overview)
  - [10.2 Redis Baselines Details (8 Total)](#102-redis-baselines-details-8-total)
  - [10.3 Redis Configurations Details (14 Total)](#103-redis-configurations-details-14-total)
  - [10.4 Redis Actions Details (19 Total)](#104-redis-actions-details-19-total)
  - [10.5 Redis CRD](#105-redis-crd)
  - [10.6 Redis Manifests](#106-redis-manifests)
  - [10.7 Redis Deployment Architecture Summary](#107-redis-deployment-architecture-summary)
- [11. Interaction Interface with OpenSaola](#11-interaction-interface-with-opensaola)
  - [11.1 How Packages Are Consumed by OpenSaola](#111-how-packages-are-consumed-by-opensaola)
  - [11.2 Role of the Operator in the Rendering Flow](#112-role-of-the-operator-in-the-rendering-flow)
  - [11.3 Status Synchronization](#113-status-synchronization)
- [12. Package Development Best Practices](#12-package-development-best-practices)

---

## 1. Architecture Overview

### 1.1 Purpose and Role of dataservice-baseline

dataservice-baseline is the middleware package repository for the data service platform. It provides standardized configuration units for deploying and managing middleware (such as Redis, MySQL, MinIO, etc.) on Kubernetes. Through declarative YAML files, it enables automated delivery and full lifecycle management of middleware.

Core responsibilities:
- Define middleware deployment modes (Baselines)
- Define Kubernetes resource templates required at runtime (Configurations)
- Define operational actions (Actions)
- Define custom resources (CRDs)
- Provide platform-side parameter configuration and internationalization (Manifests)

### 1.2 Middleware Package Directory Structure Convention

```
middleware-name/
  metadata.yaml              # [Required] Package Metadata
  icon.svg                   # [Recommended] Package Icon
  baselines/                 # [Required] Baseline Configurations
    operator-*.yaml          #   [Optional] Operator Baseline (MiddlewareOperatorBaseline)
    *.yaml                   #   [Required] Instance Baseline (MiddlewareBaseline)
  configurations/            # [Optional] Configuration Templates
    *.yaml                   #   MiddlewareConfiguration Resources
  crds/                      # [Optional] Custom Resource Definitions
    *.yaml                   #   CRDs Wrapped in MiddlewareConfiguration
  actions/                   # [Optional] Action Definitions
    pre-*.yaml               #   PreActions
    *.yaml                   #   OpsActions / NormalActions
  manifests/                 # [Optional] Parameter Manifests
    *parameters.yaml         #   Parameter Definitions (Form Field Metadata)
    i18n.yaml                #   Internationalization Translations
    middlewareversionrule.yaml  # Version Upgrade/Downgrade Rules
    hiddenmenus.yaml         #   Hidden Menu Items
```

A minimal package requires only `metadata.yaml` + 1 baseline file to be publishable.

### 1.3 Component Relationship Diagram

```
                   metadata.yaml
                       |
                       | (Package Info)
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
                       | (Reference Matching: metadata.name)
                       v
               manifests/ (Platform Side)
               - parameters (Form Parameters)
               - i18n (Internationalization)
               - middlewareversionrule (Version Rules)
```

**Core Reference Principle**: All references are matched by the `metadata.name` field, not by filename.

### 1.4 Interaction Flow with OpenSaola

1. **Package Publishing**: Middleware packages are packaged into TAR-format Secrets via saola-cli and uploaded to the Kubernetes cluster.
2. **Package Parsing**: OpenSaola parses the package content from the Secret, extracting baselines, configurations, actions, etc.
3. **Instance Creation**: When users create a middleware instance through the platform, OpenSaola renders parameters and configurations based on the selected baseline.
4. **Resource Rendering**:
   - Merge user inputs (Necessary) with Parameters defined in the baseline
   - Inject parameters into Configuration templates via configurations[].values
   - Render the final Kubernetes resources using the Go template engine
5. **Resource Creation**: The rendered Kubernetes resources are applied to the cluster.
6. **Status Synchronization**: OpenSaola continuously monitors the middleware CRD status and synchronizes it into the Middleware resource's status.

---

## 2. metadata.yaml Specification

### 2.1 Complete Field Description

| Field | Type | Required | Description | Constraints |
|-------|------|----------|-------------|-------------|
| `name` | string | Yes | Middleware name, used for platform display | Must not be empty; recommended to capitalize first letter |
| `version` | string | Yes | Package version number, format `<middleware-version>-<major>.<minor>.<patch>` | Must follow the format, e.g., `2.19.2-1.0.0` |
| `app.version` | []string | Yes | List of supported middleware versions | Must contain at least one version |
| `app.deprecatedVersion` | []string | No | List of deprecated versions | Optional, used for version migration prompts |
| `owner` | string | Yes | Vendor/developer name | Must not be empty |
| `type` | string | Yes | Middleware type classification | Allowed values: `db` / `cache` / `mq` / `search` / `storage` |
| `chartType` | string | No | Chart type identifier (optional/legacy field, not parsed by current OpenSaola version) | Usually same as name |
| `description` | string | Yes | Package description | Brief description of middleware functionality |
| `changes` | string | No | Changelog (optional/legacy field, not parsed by current OpenSaola version) | Multi-line text recording version change history |

---

## 3. Baselines System

### 3.1 Definition and Purpose of Baselines

A Baseline is the complete definition of a middleware deployment mode. There are two types:

1. **MiddlewareOperatorBaseline** (Operator Baseline): Defines the Operator controller's deployment configuration, including GVK, global variables (globe), RBAC permissions, Deployment configuration, etc.
2. **MiddlewareBaseline** (Instance Baseline): Defines the middleware instance's deployment mode, including user input parameters (necessary), instance resource configuration (parameters), pre-actions (preActions), configuration template references (configurations), etc.

Each instance baseline references an Operator baseline via `operatorBaseline.name` (Operator mode) or directly defines a `gvk` field (non-Operator mode).

### 3.2 Baseline File Format Details

#### MiddlewareOperatorBaseline Key Fields

```yaml
apiVersion: middleware.cn/v1
kind: MiddlewareOperatorBaseline
metadata:
  name: <unique-name>          # Referenced by instance baselines via operatorBaseline.name
  annotations:
    baselineName: <display-name>   # Display name in platform UI
    description: <description>      # Baseline description
spec:
  gvks:                      # Supported API resource types
    - name: <GVK-name>        # gvkName used for reference
      group: <API Group>
      version: <API Version>
      kind: <Resource Kind>
  globe:                     # Global configuration (lowercase fields, accessed via .Globe.<fieldName> in templates)
    repository: <image-registry>
    project: <project-name>
  preActions: []             # Operator pre-actions
  permissionScope: <Cluster|Namespace>  # Permission scope
  permissions: []            # RBAC permission rules
  deployment:                # Operator Deployment configuration
    spec: ...
  configurations: []         # Referenced configuration templates
```

#### MiddlewareBaseline Key Fields

```yaml
apiVersion: middleware.cn/v1
kind: MiddlewareBaseline
metadata:
  name: <unique-name>
  labels: {}
  annotations:
    baselineName: <display-name>
    description: <description>
    mode: <HA|empty>            # Marks high-availability mode
    active: <"2"|empty>         # Marks active-active mode
    datapath: <data-path-template>
spec:
  operatorBaseline:          # Operator mode
    name: <Operator-baseline-name>
    gvkName: <GVK-name>
  gvk:                       # Non-Operator mode (mutually exclusive with operatorBaseline)
    group: <API Group>
    version: <API Version>
    kind: <Resource Kind>
  necessary: {}              # User input parameter definitions (JSON-format form fields)
  preActions: []             # Pre-action list
  parameters: {}             # Instance resource configuration (passed to CRD or native K8s resources)
  configurations: []         # Referenced configuration templates and injected values
```

### 3.3 Baseline Value Merging Logic

The merging priority of fields within a Baseline is detailed in [8. Rendering Override Priority](#8-rendering-override-priority).

**Merge Strategy Summary**:
- The OperatorBaseline's `globe` field is injected by the system into the `.Globe` namespace (lowercase fields), available only in the Operator context
- The system automatically injects 6 uppercase fields (Name, Namespace, Labels, Annotations, PackageName, MiddlewareName) into the `.Globe` namespace, available in all contexts
- Template variables in `parameters` are replaced at render time with actual user input values from `.Necessary`
- Template variables in `configurations[].values` are similarly replaced, then injected as `.Values` into Configuration templates

---

## 4. Configurations System

### 4.1 Configuration File Format and Loading Mechanism

Each Configuration file is a YAML of type `MiddlewareConfiguration`, containing:

```yaml
apiVersion: middleware.cn/v1
kind: MiddlewareConfiguration
metadata:
  name: <unique-name>        # Referenced by baseline's configurations[].name
spec:
  template: |             # Kubernetes resource template in Go Template format
    <K8s Resource YAML>
```

**Loading Mechanism**:
1. The Baseline's `configurations[]` list declares which Configurations to reference
2. The system looks up Configuration files matching `metadata.name` via `configurations[].name`
3. `configurations[].values` is injected as the `.Values` variable
4. The Go template engine renders the `spec.template` content
5. The rendered result is applied to the cluster as a Kubernetes resource

### 4.2 Configuration Type Classification

By purpose:

| Type | Description | Examples |
|------|-------------|----------|
| CRD Definition | Custom Resource Definitions | crd-redisclusters, crd-mysqlcluster |
| Connection Credential | Generate connection Secrets | redis-credential, mysql-connection-credential |
| Configuration File | Runtime configuration as ConfigMap | redis-config, mysql-config |
| Monitoring | ServiceMonitor / PrometheusRule | redis-servicemonitor, mysql-alertrule |
| Service Exposure | Service / Ingress | redis-operator-service, minio-service |
| Dashboard | Grafana Dashboard ConfigMap | redis-operator-dashboard, minio-dashboard |
| Storage Class | StorageClass multi-storage mapping | redis-sc, mysql-sc |
| Other | ServiceAccount / Job, etc. | minio-serviceaccount, minio-setup |

By context:

| Context | Available Variables | Typical Files |
|---------|-------------------|---------------|
| Operator Context | `.Globe.*` (including repository/project) + `.Values.*` | operator-service, operator-dashboard, CRD |
| Instance Context | `.Globe.*` (excluding repository/project) + `.Values.*` | redis-config, mysql-config, alertrule |

### 4.3 Go Template Syntax Usage

Common Go template syntax used in Configuration templates:

**Basic Variable References**:
- `{{ .Globe.Name }}`: Instance name
- `{{ .Globe.Namespace }}`: Namespace
- `{{ .Values.xxx }}`: Parameters injected from the baseline

**Conditional Rendering**:
- `{{- if .Values.xxx }}...{{- end }}`
- `{{- if eq .Values.type "sentinel" }}...{{- end }}`

**Loops**:
- `{{- range $key, $value := .Globe.Labels }}{{ $key }}: {{ $value }}{{- end }}`
- `{{- range $i := until (int .Values.redis.replicas) }}...{{- end }}`

**Functions**:
- `{{ .Values.xxx | default 1000 }}`: Default value
- `{{ toYaml . | nindent 12 }}`: YAML serialization with indentation
- `{{ b64dec .Values.encodePassword }}`: Base64 decoding
- `{{ semverCompare ">=6.2.6" .Values.version }}`: Semantic version comparison
- `{{ contains "," .Values.storageClassName }}`: String containment check
- `{{ toCue . }}`: Convert to CUE format (used in Action steps)

**Capability Detection**:
- `{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}`: Detect whether a specific API is installed in the cluster

**Custom define/include**:
- `{{- define "redis.cal.memory" -}}...{{- end -}}`: Define reusable templates
- `{{ include "redis.maxMemory" . }}`: Invoke custom templates

### 4.4 Template Variable Sources and Rendering Flow

```
User Input (Middleware.spec.necessary)
    |
    v
MiddlewareBaseline.spec.necessary (Defines fields and default values)
    |
    +---> .Necessary.* (Used in parameters and configurations[].values)
    |
    v
MiddlewareBaseline.spec.parameters (Renders .Necessary references)
    |
    +---> .Parameters.* (Used in configurations[].values)
    |
    v
MiddlewareBaseline.spec.configurations[].values (Renders .Necessary and .Parameters references)
    |
    +---> .Values.* (Injected into Configuration templates)
    |
    v
MiddlewareConfiguration.spec.template (Can only access .Values and .Globe)
    |
    v
Final Kubernetes Resource YAML
```

---

## 5. Actions System

### 5.1 Action Definition Format and Field Description

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `metadata.name` | string | Yes | Unique Action identifier, referenced by baseline's preActions[].name |
| `metadata.annotations.alias` | string | No | Display name in platform UI |
| `metadata.annotations.ignore` | string | No | When "true", hidden in the UI |
| `metadata.annotations.roles` | string | No | Applicable role list, comma-separated |
| `spec.baselineType` | string | Yes | `PreAction` / `NormalAction` (only these two constants are defined in source code; `OpsAction` is not an explicit type in source code, but is used in actual YAML files (e.g., restart.yaml), and the controller handles it via `!= PreAction` exclusion logic) |
| `spec.actionType` | string | Yes | Action type identifier, e.g., affinity / tolerations / restart / failover / expose / migrate |
| `spec.supportedBaselines` | []string | No | Restricts this Action to specific baselines only |
| `spec.necessary` | map | No | Input parameter definitions for the Action |
| `spec.steps` | []Step | Yes | List of execution steps |

**Step Fields**:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Step name, used for `.Step.<name>` references |
| `type` | string | Step type: `KubectlEdit` / `KubectlGet` / `KubectlExec` (required when using the cue field) |
| `cue` | string | CUE expression, defining resource edit/get operations |
| `cmd` | object | Shell command definition (no type field needed) |
| `http` | object | HTTP request definition (no type field needed) |
| `output.type` | string | Output type: `string` / `json` / `yaml` |
| `output.expose` | bool | Whether to expose to subsequent steps |

### 5.2 Action Type Classification

**PreAction**: Executed before instance creation, used for configuring affinity, tolerations, logging, etc. Referenced via baseline's `preActions[]`.

**OpsAction (Operational Action)**: Operations executed during maintenance, such as restart, failover, and node migration.

**NormalAction**: Other types of operations, such as service exposure, data security configuration, and parameter settings.

### 5.3 Action Execution Mechanism

1. **PreAction Execution Timing**: When creating a Middleware instance, executed in the declaration order of `preActions[]` in the baseline
2. **Parameter Sources**:
   - PreAction: From `preActions[].parameters` + `Middleware.spec.necessary`
   - OpsAction/NormalAction: From `MiddlewareAction.spec.necessary` (user input at execution time)
3. **Step Execution**: Executed sequentially in `steps[]` array order; data is passed between steps via `.Step.<name>.output`
4. **CUE Steps**: The `output` block in KubectlEdit steps defines the resource content to patch; the `resource` block defines the target resource
5. **CMD Steps**: The `command` array is joined with spaces and executed via `sh -c`

---

## 6. CRD Definitions

### 6.1 CRDs Bundled with Each Middleware

**Redis CRDs (2)**:

| File | metadata.name | Full CRD Name | Kind | Description |
|------|---------------|---------------|------|-------------|
| redis-crd.yaml | crd-redisclusters | redisclusters.redis.middleware.hc.cn | RedisCluster | Redis Cluster Primary CRD |
| shake-crd.yaml | crd-redisshakes | redisshakes.redis.middleware.hc.cn | RedisShake | Redis Data Sync (RedisShake) CRD |

**MySQL CRDs (4)**:

| File | metadata.name | Full CRD Name | Kind | Description |
|------|---------------|---------------|------|-------------|
| mysqlcluster.yaml | crd-mysqlcluster | mysqlclusters.mysql.middleware.harmonycloud.cn | MysqlCluster | MySQL Cluster Primary CRD |
| replicate.yaml | crd-mysqlreplicate | mysqlreplicates.mysql.middleware.harmonycloud.cn | MysqlReplicate | MySQL Replication CRD |
| mysqlbackup-crd.yaml | crd-mysqlbackup | mysqlbackups.mysql.middleware.harmonycloud.cn | MysqlBackup | MySQL Backup CRD |
| mysqlbackupschedule-crd.yaml | crd-mysqlbackupschedule | mysqlbackupschedules.mysql.middleware.harmonycloud.cn | MysqlBackupSchedule | MySQL Scheduled Backup CRD |

**MinIO CRDs**: None (uses non-Operator mode, deploying native StatefulSets directly)

### 6.2 Relationship with OpenSaola CRDs

CRD files are wrapped in `MiddlewareConfiguration` and referenced via the OperatorBaseline's `configurations[]`. During Operator deployment, these CRDs are applied to the cluster for use by the Operator controller.

Special annotations in CRDs define status mapping logic:
- `harmonycloud-middleware-status-phase`: JQ expression for inferring phase status from the CRD object's annotations
- `harmonycloud-middleware-status-process`: Extracts progress information from annotations
- `harmonycloud-middleware-status-log`: Extracts log information from annotations

OpenSaola uses these annotations to synchronize middleware CRD status into the `Middleware` resource's status.

---

## 7. Manifests Directory

### 7.1 middlewareversionrule Format and Usage

The version rule file defines middleware version grouping and upgrade/downgrade paths:

```yaml
mainVersionGroups:          # Version groups
  "<group-name>":           # Group identifier
    - "<specific-version>"  # List of versions belonging to this group

crossUpgradeRules:          # Cross-version upgrade rules
  "<source-group>":
    - "<target-group>"      # Allowed upgrade target groups

crossDowngradeRules:        # Cross-version downgrade rules
  "<source-group>":
    - "<target-group>"      # Allowed downgrade target groups
```

### 7.2 parameters File (Form Parameter Definitions)

Parameters files define parameter metadata configurable on the platform interface, used by the frontend to generate configuration forms.

```yaml
middlewareDefinition: <baseline-name>   # Corresponds to MiddlewareBaseline's metadata.name
values:
  - name: <group-name>
    path: spec|configurations|<config-name>|values|args  # Parameter path within the Middleware
    parameters:
      - <parameter-name>:
          - name: default
            value: <default-value>
          - name: isReboot
            value: <y|n>         # Whether restart is required to take effect
          - name: range
            value: <value-range>  # e.g., [0-100000] or [no|yes]
          - name: describe
            value: <parameter-description>
```

### 7.3 i18n Internationalization

i18n.yaml defines the translation lookup table for strings requiring translation:

```yaml
baselineName:               # Baseline name translations
  - name: "<English>"
    name-zh: "<Chinese>"

description:                # Description translations
  - name: "<English>"
    name-zh: "<Chinese>"

placeholder:                # Placeholder translations
label:                      # Label translations
actionAliasName:            # Action alias translations
```

### 7.4 hiddenmenus Configuration

Only MinIO uses `hiddenmenus.yaml`, which defines menu items to be hidden in the platform UI:

```yaml
hiddenMenus:
  - dataSecurity       # Hide data security menu
  - disasterBackup     # Hide disaster recovery menu
  - parameter          # Hide parameter configuration menu
```

Redis and MySQL do not use this file.

---

## 8. Rendering Override Priority

### 8.1 Value Sources at Each Level

The middleware package rendering process involves multiple levels of values:

| Level | Source | Purpose | Examples |
|-------|--------|---------|----------|
| System-Injected Globe | Automatically injected by OpenSaola | Provides instance metadata | `.Globe.Name`, `.Globe.Namespace` |
| Operator Globe | OperatorBaseline.spec.globe | Provides global configuration such as image registry | `.Globe.repository`, `.Globe.project` |
| Necessary | User input when creating an instance | User-controllable parameters | `.Necessary.password`, `.Necessary.resource.mysql.replicas` |
| Parameters | MiddlewareBaseline.spec.parameters | Instance resource definition | `.Parameters.type`, `.Parameters.replicas` |
| Values | Baseline.configurations[].values | Configuration template parameters | `.Values.args.maxmemory` |
| Capabilities | System environment capability detection | K8s cluster capabilities | `.Capabilities.APIVersions`, `.Capabilities.KubeVersion` |

### 8.2 Override Priority Chain (Low to High)

```
  1. Hard-coded default values in Baseline (lowest priority)
     |
  2. Default values defined in Baseline.spec.parameters
     |
  3. | default fallback values in Configuration templates
     |
  4. Values injected via Baseline.configurations[].values
     |
  5. Values provided by the user via Middleware.spec.necessary
     |
  6. PreAction CUE patches on the Middleware resource (highest priority)
```

> **Key Note**: The source code execution order is TemplateParseWithBaseline (parse Necessary and render Parameters) -> HandlePreActions (execute CUE patches) -> handleExtraResource (render Configurations). PreAction CUE patches are deep-merged onto the Middleware object via MergeMap and can override values derived from Necessary, giving PreAction patches a higher effective priority than user Necessary values.

**Detailed Explanation**:

**Level 1: Baseline Hard-coded Values**
- Values directly specified in the Baseline file, such as `type: sentinel`, `servicePort: 6379`
- These values are not overridden by users; they are inherent characteristics of the deployment mode

**Level 2: Parameters Default Values**
- For fields in Parameters that reference `.Necessary.*`, if the JSON definition in Necessary includes a `"default"` value and the user has not provided input, the default value is used
- Example: In `replicas: "{{ .Necessary.resource.redis.replicas }}"`, the Necessary definition has `"default": 2`

**Level 3: Configuration Template default**
- Fallback default values provided via `{{ .Values.xxx | default <value> }}` in templates
- Only takes effect when `.Values.xxx` is empty/nil
- Example: `maxclients: {{ .Values.args.maxclients | default 15000 }}`

**Level 4: Baseline configurations[].values**
- Parameter values injected by the Baseline into Configurations
- Can contain fixed values or template references
- Example: `args: { maxmemory-policy: volatile-lru }`

**Level 5: User Input Necessary**
- Parameter values provided by the user when creating an instance
- Overrides the default values in the Necessary JSON definition
- Referenced via `{{ .Necessary.xxx }}` in parameters and configurations[].values

**Level 6: PreAction CUE Patch**
- PreActions execute during the HandlePreActions phase, after Necessary parsing
- PreActions modify the Middleware resource via CUE expressions in KubectlEdit steps
- Patch results are deep-merged onto the Middleware object via MergeMap, and can override parameters values derived from Necessary (e.g., affinity and toleration configurations)
- PreActions with exposed=true allow users to modify parameters
- PreActions with exposed=false use fixed parameters defined in the baseline

### 8.3 Merge Strategy (Deep Merge vs Replace)

| Scenario | Merge Strategy | Description |
|----------|---------------|-------------|
| PreAction KubectlEdit output | Deep Merge (Patch) | Fields in the CUE output block are JSON merge-patched with the target resource |
| configurations[].values | Array Deep Merge (MergeArray) | Map-type elements are matched by `name` or `serviceAccountName` fields and then deep-merged; when no matching key exists, elements are merged by positional index; unmatched elements are appended to the end of the array; non-map-type arrays are replaced entirely |
| Necessary user input | Replace | User-provided values directly replace the default values in the JSON definition |
| Go template `\| default` | Fallback | The default value is used only when the original value is empty/nil/false |

> **StructMerge Merge Direction**:
> - Invocation: `StructMerge(old=baseline, new=Middleware)`
> - Existing values in Middleware override baseline values (Middleware takes priority)
> - When a Middleware field is null/empty, the baseline value is used to fill it
> - The merge result is written back to the Middleware object
> - This rule applies to all merge scenarios including parameters, configurations, labels, and annotations

### 8.4 Template Variable Resolution Order

Variable resolution occurs in the following phases, executed in order:

**Phase 1: TemplateParseWithBaseline**
- Merge baseline and Middleware parameters, configurations, labels, annotations, and preActions
- Render parameters templates (resolve .Necessary.* references)
- Render configurations[].values templates (resolve .Necessary.* and .Parameters.* references)
- Render metadata templates (Go template expressions in labels and annotations are also resolved in this phase)

**Phase 2: HandlePreActions**
- Execute PreAction steps, which can modify Middleware parameters via CUE patches
- Note: This phase does not re-render configurations[].values; those values are finalized in Phase 1

**Phase 3: handleExtraResource + buildCustomResource**
- Render MiddlewareConfiguration.spec.template, using the already-rendered configurations[].values from Phase 1 as .Values
- Build the CR, publishing the parameters modified in Phase 2 as the CR's spec field

### 8.5 Conflict Resolution Strategy

| Conflict Type | Resolution |
|--------------|------------|
| Necessary default vs User input | User input takes priority |
| Parameters fixed value vs Necessary reference | Runtime value from Necessary reference takes priority |
| Configuration `\| default` vs values injection | Values injection takes priority (default is only a fallback) |
| PreAction patch vs Necessary-derived parameters | PreAction patch value takes priority after deep merge (PreAction executes after Necessary parsing, giving it the highest priority) |
| Multiple PreActions modifying the same field | In execution order (array order); later executions override earlier ones |

### 8.6 Practical Example

Using the maxmemory configuration in Redis Sentinel mode as an example, tracing the complete value propagation chain:

```
1. User Input Necessary:
   resource.redis.limits.memory = "4"  (4 Gi)

2. Baseline.parameters Reference:
   pod[0].resources.limits.memory = '"{{ .Necessary.resource.redis.limits.memory }}"'
   -> Resolves to "4"

3. Baseline.configurations[].values Passed to redis-config:
   values:
     redis:
       resources:
         limits:
           memory: '"{{ .Necessary.resource.redis.limits.memory }}"'
   -> Resolves to { redis: { resources: { limits: { memory: "4" } } } }

4. redis-config Configuration Template Rendering:
   {{- define "redis.cal.memory" -}}
     ... calculate 80% of memory ...
   {{- end -}}
   
   {{- define "redis.maxMemory" -}}
     {{- if .Values.args.maxmemory }}          <- Check if user customized maxmemory
       {{- .Values.args.maxmemory }}           <- Prefer user-defined value
     {{- else }}
       {{- include "redis.cal.memory" (list .Values.redis.resources.limits.memory "max") }}
                                               <- Otherwise auto-calculate 80% based on memory limit
     {{- end }}
   {{- end -}}
   
   maxmemory {{ include "redis.maxMemory" . }}
   -> Final value: maxmemory 3276mb  (4Gi * 80% ~= 3276 MiB)

5. If the user modifies args.maxmemory via parameters:
   args:
     maxmemory: "2gb"
   -> Final value: maxmemory 2gb  (user-defined value takes priority)
```

---

## 9. Secret Structure and Labels

### 9.1 connection-credential Common Patterns

Existing middleware packages typically include a connection credential Configuration that generates a corresponding Secret upon instance creation. This is not a platform requirement but a recommended practice; each middleware can define its own field structure. The format is as follows:

```yaml
apiVersion: v1
kind: Secret
type: Opaque
metadata:
  name: <instance-name>-credential
  namespace: <namespace>
stringData:
  endpoint: <service-address>
  port: "<service-port>"
  username: <username>          # Present in MinIO/MySQL, absent in Redis
  password: "<password>"
```

### 9.2 Secret Data Field Comparison Across Middleware

| Middleware | endpoint Format | port | username | password |
|-----------|----------------|------|----------|----------|
| Redis | `<name>.<namespace>.svc` | 6379 | None | Redis requirepass |
| MinIO | `<name>-svc.<namespace>.svc` | 9000 | minio | MinIO SecretKey |
| MySQL | `<name>.<namespace>.svc` | 3306 | root | MySQL root password |

**The following are implementation details specific to the Redis package connection-credential template, not platform-wide features**:
- Support for `encodePassword` (Base64-encoded password): If present, the `b64dec`-decoded value is used preferentially
- `redisServicePort`: A custom port can override the default `redis.port` via values

### 9.3 Labels and Annotations

#### Platform-Injected Labels

`consts.Label*` series labels automatically set by OpenSaola:
- `middleware.cn/component`: Component identifier
- `middleware.cn/packageversion`: Package version
- `app.kubernetes.io/instance`: Instance identifier
- `app.kubernetes.io/name`: Middleware name

#### Custom Labels/Annotations Examples by Middleware

**Baseline-Level Labels**:
- `nephele/user: admin`: From Redis baseline
- `app: {{ .Globe.Name }}`: From MinIO baseline

**Baseline-Level Annotations**:
- `baselineName`: Baseline display name
- `description`: Baseline description
- `mode: "HA"`: High-availability marker
- `active: "2"`: Active-active marker
- `datapath`: Data path template (from Redis)

**Operator Deployment Labels**:
- `app.kubernetes.io/name: <middleware>-operator`
- `app.kubernetes.io/instance: {{ .Globe.Name }}`

**Instance Pod Labels** (from parameters):
- `app: {{ .Globe.Name }}`
- `middleware: redis` (from Redis) / `operatorname: mysql-operator` (from MySQL)
- `harmonycloud.cn/statefulset: {{ .Globe.Name }}`

**Instance Pod Annotations** (from parameters):
- `fixed-node-middleware-pod: "true"`: Fixed node marker
- `fixed.ipam.harmonycloud.cn: "true"`: Fixed IP marker

---

## 10. Redis Complete Adaptation Case Study

This chapter uses Redis as a complete example to demonstrate the adaptation details of a middleware package. Other middleware (MinIO, MySQL) follow similar adaptation patterns and can be understood by referring to this chapter's structure.

### 10.1 Redis Package Overview

**metadata.yaml Key Information**:
- name: Redis
- version: 2.19.2-1.0.0
- app.version: 8.2.3, 8.0.4, 7.4.6, 7.2.11, 7.2.4 (5 versions)
- owner: HarmonyCloud
- type: db

**Directory Structure Summary**:

| Directory | File Count | Description |
|-----------|-----------|-------------|
| baselines/ | 8 | 2 Operator baselines + 6 instance baselines |
| configurations/ | 14 | Connection credentials, configs, monitoring, services, etc. |
| actions/ | 19 | 11 PreActions + 2 OpsActions + 6 NormalActions |
| crds/ | 2 | RedisCluster + RedisShake |
| manifests/ | 8 | 6 parameters + i18n + middlewareversionrule |

### 10.2 Redis Baselines Details (8 Total)

#### 10.2.1 Operator Baselines (2)

**redis-operator-standard** (Standard Operator)
- File: `baselines/operator-standard.yaml`
- GVK: `redis.middleware.hc.cn/v1alpha1/RedisCluster`
- Globe: `repository: 10.10.101.172:443`, `project: middleware`
- Deployment: replicas=1, resources 200m CPU / 512Mi Memory
- Image: `redis-operator:v2.19.1`
- preActions: pre-redis-operator-alertrule-labels, pre-redis-operator-affinity (soft), pre-redis-operator-tolerations
- Configurations referenced: crd-redisclusters, crd-redisshakes, redis-operator-alertrule, redis-operator-dashboard, redis-operator-service, redis-operator-servicemonitor

**redis-operator-highly-available** (Highly Available Operator)
- File: `baselines/operator-highly-available.yaml`
- Difference from standard version: **replicas=3** (3 replicas for high availability)
- All other configurations are identical (same GVK, globe, permissions, preActions, configurations)

#### 10.2.2 Instance Baselines (6)

**redis-sentinel** (Sentinel Mode)
- File: `baselines/sentinel.yaml`
- metadata.name: `redis-sentinel`
- Referenced Operator: `redis-operator-standard`
- Deployment type (parameters.type): `sentinel`
- Resource components: Redis nodes + Sentinel nodes (supports Deployment or StatefulSet deployment methods)
- Necessary definitions: repository, version (default 8.2.3), password, resource.redis (CPU/Memory/Replicas/Volume), resource.sentinel (CPU/Memory/Replicas/DeployMethod)
- Parameters key fields:
  - `type: sentinel`
  - `sentinel.replicas/resources`: From `.Necessary.resource.sentinel.*`
  - `pod[0].middlewareImage`: Complex version mapping logic (selects different image tags based on Redis version)
  - `pod[0].resources`: From `.Necessary.resource.redis.*`
  - `replicas`: From `.Necessary.resource.redis.replicas`
  - `volumeClaimTemplates`: Single volume redis-data
- Configurations referenced (10): redis-credential, redis-predixy-servicemonitor, redis-alertrule, redis-config, redis-sc, redis-servicemonitor, redis-shard-sc, redis-sentinel-configmap, redis-metrice-svc (Note: spelling as in source code)

**redis-sentinel-proxy** (Sentinel Proxy Mode)
- File: `baselines/sentinel-proxy.yaml`
- metadata.name: `redis-sentinel-proxy`
- Differences from the sentinel baseline:
  - Additional `predixy` component configuration (proxy layer)
  - Necessary additionally includes CPU/Memory definitions for `resource.predixy`
  - Parameters include an additional `predixy` configuration block (image, port 7617, replicas 3, etc.)
  - `replicas` uses `{{ div .Necessary.resource.redis.replicas 2 }}` (sharding mode, replica count halved)
  - Additionally references redis-predixy-configmap Configuration

**redis-sentinel-active** (Sentinel Active-Active Mode)
- File: `baselines/sentinel-active.yaml`
- metadata.name: `redis-sentinel-active`
- Annotations include `active: "2"` (active-active identifier)
- Differences from the sentinel baseline:
  - `resource.redis.volumes` (multi-volume storage, replacing single volume.storageClass)
  - `storageClassName` uses range loop to concatenate multiple storage classes
  - `storage` obtained via `index .Necessary.resource.redis.volumes 0 "size"`

**redis-sentinel-proxy-active** (Sentinel Proxy Active-Active Mode)
- File: `baselines/sentinel-proxy-active.yaml`
- metadata.name: `redis-sentinel-proxy-active`
- Combines features of sentinel-proxy and sentinel-active (active-active + proxy)
- Annotations include `active: "2"`

**redis-cluster** (Cluster Mode)
- File: `baselines/cluster.yaml`
- metadata.name: `redis-cluster`
- Deployment type (parameters.type): `cluster`
- Differences from sentinel mode:
  - No Sentinel component
  - Redis nodes default to 6 (`replicas` defaults to 6, minimum 3)
  - Cluster mode redis.conf includes `cluster-enabled yes`
- Necessary only includes redis resources (no sentinel)

**redis-cluster-proxy** (Cluster Proxy Mode)
- File: `baselines/cluster-proxy.yaml`
- metadata.name: `redis-cluster-proxy`
- Combines features of cluster and proxy
- Additional predixy component and configuration
- Additionally references redis-predixy-configmap

### 10.3 Redis Configurations Details (14 Total)

| Filename | metadata.name | Generated K8s Resource | Key Values References |
|----------|---------------|----------------------|----------------------|
| connection-credential.yaml | redis-credential | Secret (Opaque) | `.Values.redis.port`, `.Values.redisPassword`, `.Values.encodePassword` |
| operator-alert.yaml | redis-operator-alertrule | PrometheusRule | `.Values.labels` |
| operator-dashboard-configmap.yaml | redis-operator-dashboard | ConfigMap (Grafana Dashboard) | No Values, pure JSON panel definition |
| operator-service.yaml | redis-operator-service | Service (ClusterIP) | No Values, uses `.Globe.Name/Labels` |
| operator-servicemonitor.yaml | redis-operator-servicemonitor | ServiceMonitor | No Values |
| proxy-servicemonitor.yaml | redis-predixy-servicemonitor | ServiceMonitor | `.Values.predixy.enableProxy` |
| redis-alert.yaml | redis-alertrule | PrometheusRule | `.Values.type`, `.Values.predixy.enableProxy`, `.Values.alertRules.*`, `.Values.clusterId`, `.Values.labels`, `.Values.customAlertRules` |
| redis-config.yaml | redis-config | ConfigMap | `.Values.type`, `.Values.version`, `.Values.redis.resources`, `.Values.args.*` (extensive Redis configuration parameters), `.Values.customVolumes`, `.Values.exporter` |
| redis-metrice-svc.yaml | redis-metrice-svc (Note: spelling as in source code) | Service (ClusterIP) | No Values |
| redis-predixy-configmap.yaml | redis-predixy-configmap | ConfigMap | `.Values.predixy.*` (enableProxy/port/workerThreads/args), `.Values.redis.port/replicas`, `.Values.redisPassword`, `.Values.type`, `.Values.sentinel.port` |
| redis-sc.yaml | redis-sc | ConfigMap (StorageClass Mapping) | `.Values.type`, `.Values.predixy.enableProxy`, `.Values.storageClassName` |
| redis-sentinel-configmap.yaml | redis-sentinel-configmap | ConfigMap | `.Values.type`, `.Values.sentinel.*` (port/replicas/args/auth), `.Values.redis.port`, `.Values.redisPassword`, `.Values.encodePassword` |
| redis-servicemonitor.yaml | redis-servicemonitor | ServiceMonitor | No Values |
| redis-shard-sc.yaml | redis-shard-sc | ConfigMap (Shard StorageClass) | `.Values.type`, `.Values.predixy.enableProxy`, `.Values.storageClassName`, `.Values.redis.replicas` |

**redis-config.yaml Special Notes**:
- Contains custom function `redis.cal.memory`: Converts memory strings (e.g., "4Gi") to bytes, calculates maxmemory (80%) and output-buffer limits
- Conditionally outputs `cluster-enabled` or `slaveof` configuration based on `.Values.type`
- Uses `semverCompare` for version feature detection (e.g., ACL file, ARM64-COW-BUG fix)
- Extensive Redis parameters use the `{{ .Values.args.xxx | default <default-value> }}` pattern

### 10.4 Redis Actions Details (19 Total)

**PreActions (11)**:

| File | metadata.name | actionType | Description |
|------|---------------|------------|-------------|
| pre-affinity.yaml | pre-redis-affinity | affinity | Redis instance Pod anti-affinity configuration |
| pre-alertrule-labels.yaml | pre-redis-alertrule-labels | alertlabel | Alert rule label injection |
| pre-hdpool.yaml | pre-redis-heimdallr-hdpool | hdpool | Heimdallr network pool configuration (IP allocation) |
| pre-hostnetwork.yaml | pre-redis-hostnetwork | hostnetwork | Host network mode configuration |
| pre-logging.yaml | pre-redis-logging | pre-logging | Logging configuration (file log + stdout) |
| pre-operator-affinity.yaml | pre-redis-operator-affinity | affinity | Operator Pod anti-affinity |
| pre-operator-alertrule-labels.yaml | pre-redis-operator-alertrule-labels | alertlabel | Operator alert rule labels |
| pre-operator-tolerations.yaml | pre-redis-operator-tolerations | tolerations | Operator Pod tolerations |
| pre-proxy-affinity.yaml | pre-redis-proxy-affinity | affinity | Predixy proxy Pod anti-affinity |
| pre-sentinel-affinity.yaml | pre-redis-sentinel-affinity | affinity | Sentinel Pod anti-affinity |
| pre-tolerations.yaml | pre-redis-tolerations | tolerations | Redis instance Pod tolerations |

**OpsActions (2)**:

| File | metadata.name | actionType | Description |
|------|---------------|------------|-------------|
| restart.yaml | redis-restart | restart | Middleware restart (detects presence of predixy, handles each case separately) |
| migrate.yaml | redis-migrate | migrate | Node migration (checks primary/replica roles, only allows migration of slave/sentinel/predixy) |

**NormalActions (6)**:

| File | metadata.name | actionType | Description |
|------|---------------|------------|-------------|
| datasecurity.yaml | redis-datasecurity | datasecurity | Data security (empty steps, marked as ignore) |
| expose-cluster-external.yaml | redis-cluster-expose-external | expose | Cluster mode external exposure (NodePort/Ingress) |
| expose-proxy.yaml | redis-proxy-expose | expose | Proxy mode read-write exposure |
| expose-sentinel-external.yaml | redis-sentinel-expose-external | expose | Sentinel mode external exposure |
| expose-sentinel-readonly.yaml | redis-sentinel-expose-readonly | expose | Sentinel mode read-only exposure |
| expose-sentinel-readwrite.yaml | redis-sentinel-expose-readwrite | expose | Sentinel mode read-write exposure |

### 10.5 Redis CRD

See [6.1 CRDs Bundled with Each Middleware](#61-crds-bundled-with-each-middleware) for Redis CRD details.

The RedisCluster CRD's status mapping annotations pass the Operator's status information (phase, process, log) to OpenSaola, enabling unified status management.

### 10.6 Redis Manifests

**parameters Files (6)**:

| File | Corresponding Baseline | Description |
|------|----------------------|-------------|
| sentinelparameters.yaml | redis-sentinel | Sentinel mode runtime parameters |
| sentinelproxyparameters.yaml | redis-sentinel-proxy | Sentinel proxy mode parameters |
| sentinelactiveparameters.yaml | redis-sentinel-active | Sentinel active-active mode parameters |
| sentinelproxyactiveparameters.yaml | redis-sentinel-proxy-active | Sentinel proxy active-active parameters |
| clusterparameters.yaml | redis-cluster | Cluster mode parameters |
| clusterproxyparameters.yaml | redis-cluster-proxy | Cluster proxy mode parameters |

Each parameters file is associated with the corresponding MiddlewareBaseline via the `middlewareDefinition` field, defining Redis configuration parameters (such as maxmemory-policy, maxclients, timeout, etc.) that can be dynamically adjusted in the platform UI, with annotations indicating whether each parameter requires a restart to take effect.

**Version Rules**: Redis's middlewareversionrule defines 6 version groups (6.x, 7.0, 7.2, 7.4, 8.0, 8.2), supporting multi-level upward upgrades (e.g., 6.x can upgrade to 8.2), but downgrades are limited to within the same group.

### 10.7 Redis Deployment Architecture Summary

The Redis package supports 6 deployment modes, covering scenarios from basic to enterprise-grade:

| Deployment Mode | Baseline Name | Characteristics | Use Case |
|----------------|---------------|-----------------|----------|
| Sentinel Mode | redis-sentinel | Redis + Sentinel, automatic failover | Standard high-availability scenarios |
| Sentinel Proxy Mode | redis-sentinel-proxy | Additional Predixy proxy layer, read-write splitting | Read-write splitting and connection pool management |
| Sentinel Active-Active Mode | redis-sentinel-active | Multi-volume storage, active-active deployment | Cross-datacenter/cross-AZ disaster recovery |
| Sentinel Proxy Active-Active Mode | redis-sentinel-proxy-active | Active-active + proxy | Cross-datacenter DR + read-write splitting |
| Cluster Mode | redis-cluster | Redis Cluster, automatic data sharding | High data volume, high throughput scenarios |
| Cluster Proxy Mode | redis-cluster-proxy | Cluster + Predixy proxy | Cluster mode + client-transparent proxy |

**Selection Guidelines**:
- Small data volume, moderate availability requirements: sentinel (simplest)
- Read-write splitting needed: sentinel-proxy
- Cross-datacenter disaster recovery: sentinel-active or sentinel-proxy-active
- Large data volume, horizontal scaling needed: cluster
- Cluster mode with client compatibility requirements: cluster-proxy

---

## 11. Interaction Interface with OpenSaola

### 11.1 How Packages Are Consumed by OpenSaola

1. **Package Upload**: Middleware packages are packaged as Kubernetes Secrets via saola-cli and pushed to the specified namespace
2. **Package Discovery**: OpenSaola discovers available middleware packages through Secret labels
3. **Package Parsing**:
   - Decompress the TAR content from the Secret
   - Parse metadata.yaml to obtain package information
   - Parse baselines/ to obtain all available deployment modes
   - Parse configurations/, actions/, crds/, manifests/ to obtain accompanying resources
4. **Package Registration**: Register the parsed package information in the platform's middleware catalog

### 11.2 Role of the Operator in the Rendering Flow

OpenSaola acts as the template engine in the rendering flow:

1. **Receive Creation Request**: User selects a baseline and fills in Necessary parameters
2. **Parse Baseline**:
   - Load the corresponding MiddlewareBaseline
   - Load the associated MiddlewareOperatorBaseline (if applicable)
3. **Inject System Variables**:
   - Inject `.Globe.Name`, `.Globe.Namespace`, and 4 other system fields
   - Inject `.Globe.repository`, `.Globe.project`, and other Operator globe fields (Operator context only)
   - Inject `.Capabilities` environment capability information
4. **Render Parameters**:
   - Replace `{{ .Necessary.xxx }}` with actual user input
   - Generate final parameters values
5. **Execute PreActions**:
   - Execute each PreAction in the preActions list in order
   - PreActions can modify the Middleware resource's parameters via KubectlEdit
6. **Render Configurations**:
   - Replace template references in configurations[].values with actual values
   - Inject the final values as `.Values`
   - Render each Configuration's template using the Go template engine
7. **Create Resources**: Apply the rendered Kubernetes resources to the cluster

> **Parameters to CR Mapping**:
> - `Middleware.spec.parameters` is published as the CR's `spec` field in its entirety
> - The CR inherits the Middleware's labels, annotations, name, and namespace
> - The JSON structure of parameters must align exactly with the target CRD's spec schema

### 11.3 Status Synchronization

OpenSaola synchronizes middleware status through the following mechanisms:

1. **CRD Status Watch**: Monitors status changes of middleware CRDs (e.g., RedisCluster, MysqlCluster)
2. **Status Mapping**: Maps status through JQ expressions in CRD annotations:
   - `harmonycloud-middleware-status-phase`: Maps the phase field
   - `harmonycloud-middleware-status-process`: Maps the process field
   - `harmonycloud-middleware-status-log`: Maps the reason/log field
3. **Middleware Status Update**: Writes the mapped status into the Middleware resource's status
4. **Non-Operator Mode** (e.g., MinIO): OpenSaola directly monitors the StatefulSet's status to determine instance state

---

## 12. Package Development Best Practices

1. **Minimalism Principle**: Include only essential files; add complex features as needed. The simplest package requires only `metadata.yaml` + 1 baseline.

2. **Naming Consistency**: Keep filenames consistent with `metadata.name` for easier maintenance and troubleshooting.

3. **Clear Variable Propagation Chain**:
   - Use `.Necessary.*` in `parameters` to reference user input
   - Use `.Necessary.*` and `.Parameters.*` in `configurations[].values` as bridges
   - Use only `.Values.*` and `.Globe.*` in Configuration templates

4. **Default Value Strategy**:
   - Set reasonable `"default"` values in Necessary JSON
   - Use `| default` in Configuration templates to provide fallback values
   - Use `"required": true` for critical parameters to enforce user input

5. **Version Compatibility**:
   - Use `semverCompare` to handle version differences
   - Centralize version-related logic in `pre-bigversion` PreActions
   - Clearly define upgrade/downgrade paths in middlewareversionrule

6. **Capability Detection**:
   - Use `{{ if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}` to detect optional features
   - Avoid creating PrometheusRule/ServiceMonitor on clusters without Prometheus Operator

7. **Security Considerations**:
   - Use `"type": "password"` type and regex validation for password fields
   - Do not store passwords in plaintext in ConfigMaps
   - Use Secret type consistently for connection credentials

8. **Observability**:
   - Configure ServiceMonitor, PrometheusRule, and Grafana Dashboard for each middleware
   - Use configurable thresholds for alert rules (injected via alertRules values)
   - Support custom alert rules (customAlertRules)

9. **Operations-Friendly**:
   - Provide restart Action and handle scenarios with proxy components
   - Node migration Action should validate role restrictions (disallow primary node migration)
   - PreAction `exposed: true` allows users to modify deployment options

10. **Internationalization**:
    - Provide Chinese and English translations in i18n.yaml for all user-facing text (baselineName, description, placeholder, label)
    - Use English for Action aliases, with translations provided in i18n
