**English** | [中文](troubleshooting_zh.md)

# Troubleshooting Guide

This guide helps diagnose and resolve common issues with OpenSaola middleware management. For detailed architecture and reconcile flow information, see the [Technical Documentation](opensaola-technical.md).

## Table of Contents

- [Resource States](#resource-states)
- [Common Issues](#common-issues)
  - [Middleware stuck in "Unavailable" state](#middleware-stuck-in-unavailable-state)
  - ["no matching MiddlewareOperator found"](#no-matching-middlewareoperator-found)
  - ["package not ready" / "package install failed"](#package-not-ready--package-install-failed)
  - [Middleware stuck in "Updating" state](#middleware-stuck-in-updating-state)
  - [Finalizer preventing deletion](#finalizer-preventing-deletion)
  - [Pod stuck in CrashLoopBackOff](#pod-stuck-in-crashloopbackoff)
  - [CustomResource automatically recreated after deletion](#customresource-automatically-recreated-after-deletion)
  - [MiddlewareAction not executing](#middlewareaction-not-executing)
- [Debugging Commands](#debugging-commands)
- [Checking Conditions](#checking-conditions)
- [Log Configuration](#log-configuration)
- [Further Reading](#further-reading)

---

## Resource States

Every OpenSaola CRD has a `status.state` field. The three possible values are:

| State | Meaning | When it occurs |
|-------|---------|---------------|
| `Available` | The resource is healthy and fully operational. | All status conditions are `True`. |
| `Unavailable` | The resource has encountered an error or is not ready. | Any status condition is `False`. For MiddlewareAction, `Unknown` conditions also trigger this state. |
| `Updating` | The resource is being upgraded to a new package version. | The `middleware.cn/update` annotation is set and the upgrade flow is in progress. |

### Phase values

The `status.customResources.phase` field on Middleware reflects the phase of the underlying custom resource:

| Phase | Meaning |
|-------|---------|
| `""` (empty) | Unknown / initial state |
| `Checking` | Resource is being validated |
| `Checked` | Validation completed |
| `Creating` | Resource is being created |
| `Updating` | Resource is being updated |
| `Running` | Resource is running normally |
| `Failed` | Resource has encountered an error |
| `UpdatingCustomResources` | Custom resources are being updated |
| `BuildingRBAC` | RBAC resources are being created |
| `BuildingDeployment` | Deployment is being created |
| `Finished` | Lifecycle operation completed |
| `MappingFields` | CUE field mapping is in progress |
| `Executing` | An action is being executed |

---

## Common Issues

### Middleware stuck in "Unavailable" state

**Symptoms**: The Middleware resource shows `State: Unavailable` and does not transition to `Available`.

**Causes**:
1. One or more status conditions have `Status: False`
2. The referenced MiddlewareBaseline does not exist or is not `Available`
3. The MiddlewareOperatorBaseline reference is invalid
4. Required `necessary` parameters are missing (e.g., `image` is required)
5. Template rendering failed during baseline merge
6. Pre-actions failed to execute

**Debugging steps**:

```bash
# 1. Check the Middleware status and conditions
kubectl get mid <name> -n <namespace> -o yaml

# 2. Find the first False condition -- its message explains the root cause
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.status == "False")'

# 3. Check the status reason field for a summary
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.reason}'

# 4. Check that the referenced baseline exists and is Available
kubectl get mb <baseline-name> -o jsonpath='{.status.state}'

# 5. Check the operator logs for detailed error messages
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=100
```

**Solution**: Address the root cause indicated by the `False` condition. Common fixes include:
- Ensure the referenced MiddlewareBaseline and MiddlewareOperatorBaseline exist
- Provide all required `necessary` fields (at minimum, `image` must be specified)
- Fix template syntax errors in parameters or configurations
- Verify that pre-action MiddlewareActionBaseline resources exist

---

### "no matching MiddlewareOperator found"

**Symptoms**: Middleware is `Unavailable`, and the conditions or operator logs indicate that no matching MiddlewareOperator was found.

**Causes**:
1. The MiddlewareOperator resource has not been created in the target namespace
2. The MiddlewareOperator's baseline reference does not match the GVK expected by the Middleware
3. The MiddlewareOperatorBaseline's `gvks` list does not contain the required GVK entry

**Debugging steps**:

```bash
# 1. List all MiddlewareOperators in the namespace
kubectl get mo -n <namespace>

# 2. Check the MiddlewareOperator's baseline and its GVKs
kubectl get mob <operator-baseline-name> -o jsonpath='{.spec.gvks}'

# 3. Compare with the Middleware's operatorBaseline.gvkName
kubectl get mid <name> -n <namespace> -o jsonpath='{.spec.operatorBaseline}'
```

**Solution**:
- Create the required MiddlewareOperator in the same namespace
- Ensure its `spec.baseline` points to a valid MiddlewareOperatorBaseline
- Verify the MiddlewareOperatorBaseline's `spec.gvks` list contains an entry whose `name` matches the Middleware's `operatorBaseline.gvkName`

---

### "package not ready" / "package install failed"

**Symptoms**: MiddlewarePackage is not `Available`, or baselines/configurations are not being created.

**Causes**:
1. The package Secret does not have the required label `middleware.cn/project: OpenSaola`
2. The Secret data is corrupted or the tar/zstd archive is malformed
3. The `middleware.cn/install` annotation is missing from the Secret
4. The Secret is in the wrong namespace (should match `config.dataNamespace`, default: `middleware-operator`)
5. CRD files within the package contain invalid definitions

**Debugging steps**:

```bash
# 1. Check if the Secret exists with the correct labels
kubectl get secret -n <data-namespace> -l middleware.cn/project=OpenSaola

# 2. Check if the MiddlewarePackage was created
kubectl get mp

# 3. Check the MiddlewarePackage status and conditions
kubectl get mp <package-name> -o yaml

# 4. Check for install errors in the Secret annotations
kubectl get secret <secret-name> -n <data-namespace> -o jsonpath='{.metadata.annotations}'

# 5. Verify the middleware.cn/enabled label is "true"
kubectl get secret <secret-name> -n <data-namespace> -o jsonpath='{.metadata.labels.middleware\.cn/enabled}'

# 6. Check operator logs for package parsing errors
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=100 | grep -i "package"
```

**Solution**:
- Ensure the Secret has the label `middleware.cn/project: OpenSaola`
- Ensure the Secret is in the configured `dataNamespace` (check the operator's `config.yaml` or Helm values `config.dataNamespace`)
- Re-upload the package using `saola-cli` if the archive is corrupted
- Check `middleware.cn/installError` annotation on the Secret for specific error details
- Check `middleware.cn/installDigest` annotation to verify the package digest

---

### Middleware stuck in "Updating" state

**Symptoms**: The Middleware shows `State: Updating` and never transitions to `Available`.

**Causes**:
1. The upgrade target package version is not available
2. The new baseline referenced by `middleware.cn/baseline` annotation does not exist
3. An error occurred during the `ReplacePackage` flow
4. The new package's template rendering or parameter merge failed

**Debugging steps**:

```bash
# 1. Check the update annotation
kubectl get mid <name> -n <namespace> -o jsonpath='{.metadata.annotations.middleware\.cn/update}'

# 2. Check the baseline annotation
kubectl get mid <name> -n <namespace> -o jsonpath='{.metadata.annotations.middleware\.cn/baseline}'

# 3. Verify the target package version exists
kubectl get mp -l middleware.cn/packageversion=<target-version>

# 4. Check conditions for the Updating condition
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.type == "Updating")'

# 5. Check operator logs
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200 | grep -i "update\|replace"
```

**Solution**:
- Ensure the target package version is published and the corresponding MiddlewareBaseline exists
- If the upgrade is stuck and you need to abort, remove the update annotation:
  ```bash
  kubectl annotate mid <name> -n <namespace> middleware.cn/update- middleware.cn/baseline-
  ```
  Note: Removing these annotations does not roll back changes already applied. You may need to manually restore the previous state.

---

### Finalizer preventing deletion

**Symptoms**: A Middleware or MiddlewareOperator resource has a `deletionTimestamp` set but is not being removed. The resource remains in a `Terminating` state.

**Background**: OpenSaola uses finalizers to ensure proper cleanup of dependent resources before a Middleware or MiddlewareOperator is deleted:
- `middleware.cn/middleware-cleanup` -- on Middleware resources
- `middleware.cn/middlewareoperator-cleanup` -- on MiddlewareOperator resources

The controller adds the finalizer when the resource is first reconciled and removes it after cleanup completes successfully.

**Causes**:
1. The operator pod is not running, so the finalizer cannot be processed
2. The cleanup logic encountered an error (e.g., failed to delete dependent custom resources)
3. The operator lacks RBAC permissions to delete dependent resources
4. A network or API server issue is preventing the cleanup

**Debugging steps**:

```bash
# 1. Check if the resource has a finalizer
kubectl get mid <name> -n <namespace> -o jsonpath='{.metadata.finalizers}'

# 2. Check the operator pod status
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>

# 3. Check operator logs for cleanup errors
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200 | grep -i "finalizer\|cleanup\|delete"

# 4. Check if dependent resources still exist
kubectl get all -n <namespace> -l middleware.cn/source=<middleware-name>
```

**Solution**:
- If the operator is running, check the logs for cleanup errors and resolve the underlying issue
- If the operator is not running, start it first and wait for the finalizer to be processed
- As a **last resort**, manually remove the finalizer (this skips cleanup and may leave orphaned resources):
  ```bash
  kubectl patch mid <name> -n <namespace> --type=json -p='[{"op": "remove", "path": "/metadata/finalizers"}]'
  ```
  After manual removal, check for and clean up any orphaned resources (Deployments, RBAC, ConfigMaps, etc.)

---

### Pod stuck in CrashLoopBackOff

**Symptoms**: The OpenSaola operator pod or a middleware operator Deployment pod is in `CrashLoopBackOff`.

**Causes**:
1. Invalid configuration or missing environment variables
2. Insufficient resources (OOMKilled)
3. CRD definitions not installed or version mismatch
4. RBAC permissions insufficient for the operator

**Debugging steps**:

```bash
# 1. Check pod status and restart count
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>

# 2. Check pod events
kubectl describe pod <pod-name> -n <operator-namespace>

# 3. Check logs from the previous crash
kubectl logs <pod-name> -n <operator-namespace> --previous --tail=100

# 4. Check if the pod was OOMKilled
kubectl get pod <pod-name> -n <operator-namespace> -o jsonpath='{.status.containerStatuses[0].lastState.terminated.reason}'

# 5. Check CRD installation
kubectl get crds | grep middleware.cn
```

**Solution**:
- If OOMKilled, increase memory limits in Helm values (`resources.limits.memory`)
- If CRDs are missing, reinstall the Helm chart with `kubectl.installCRDs: true`
- Check the crash logs for specific error messages and address accordingly
- Verify RBAC permissions are correctly configured

---

### CustomResource automatically recreated after deletion

**Symptoms**: You deleted a CustomResource managed by a Middleware, but it was immediately recreated.

**Cause**: This is expected behavior. The CR Watcher detects the deletion and checks if the owning Middleware still exists. If the Middleware exists, the CR is automatically rebuilt to maintain the desired state.

**Solution**:
- To permanently remove the CustomResource, delete the owning Middleware resource instead
- If you need to modify the CR, update the Middleware spec rather than editing the CR directly

---

### MiddlewareAction not executing

**Symptoms**: A MiddlewareAction was created but nothing happens.

**Causes**:
1. The MiddlewareAction's `status.state` is already set (non-empty). MiddlewareAction is a one-shot resource -- once it has a state, it will not re-execute
2. The `spec.baseline` references a non-existent MiddlewareActionBaseline
3. The baseline's `baselineType` is `PreAction`, which is only executed within the Middleware/MiddlewareOperator reconcile flow, not independently

**Debugging steps**:

```bash
# 1. Check the current state
kubectl get ma <name> -n <namespace> -o jsonpath='{.status.state}'

# 2. Check the referenced baseline exists
kubectl get mab <baseline-name>

# 3. Check the baseline type
kubectl get mab <baseline-name> -o jsonpath='{.spec.baselineType}'

# 4. Check conditions for step-level errors
kubectl get ma <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.'
```

**Solution**:
- If you need to re-execute an action, delete the existing MiddlewareAction and create a new one
- Ensure the baseline's `baselineType` is `NormalAction` (or `OpsAction`) for standalone execution

---

## Debugging Commands

### Quick reference by CRD

| Resource | Short name | Scope | List all | Describe | Check state |
|----------|-----------|-------|----------|----------|-------------|
| Middleware | `mid` | Namespaced | `kubectl get mid -A` | `kubectl describe mid <name> -n <ns>` | `kubectl get mid <name> -n <ns> -o jsonpath='{.status.state}'` |
| MiddlewareBaseline | `mb` | Cluster | `kubectl get mb` | `kubectl describe mb <name>` | `kubectl get mb <name> -o jsonpath='{.status.state}'` |
| MiddlewareOperator | `mo` | Namespaced | `kubectl get mo -A` | `kubectl describe mo <name> -n <ns>` | `kubectl get mo <name> -n <ns> -o jsonpath='{.status.state}'` |
| MiddlewareOperatorBaseline | `mob` | Cluster | `kubectl get mob` | `kubectl describe mob <name>` | `kubectl get mob <name> -o jsonpath='{.status.state}'` |
| MiddlewarePackage | `mp` | Cluster | `kubectl get mp` | `kubectl describe mp <name>` | `kubectl get mp <name> -o jsonpath='{.status.state}'` |
| MiddlewareAction | `ma` | Namespaced | `kubectl get ma -A` | `kubectl describe ma <name> -n <ns>` | `kubectl get ma <name> -n <ns> -o jsonpath='{.status.state}'` |
| MiddlewareActionBaseline | `mab` | Cluster | `kubectl get mab` | `kubectl describe mab <name>` | `kubectl get mab <name> -o jsonpath='{.status.state}'` |
| MiddlewareConfiguration | `mc` | Cluster | `kubectl get mc` | `kubectl describe mc <name>` | `kubectl get mc <name> -o jsonpath='{.status.state}'` |

### Operator logs

```bash
# View operator logs (tail)
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200

# Follow operator logs in real time
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> -f

# Filter for a specific middleware type
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=500 | grep "redis"

# Filter for errors only
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=500 | grep -i "error\|fail"
```

### Package-related checks

```bash
# List all package Secrets
kubectl get secret -n <data-namespace> -l middleware.cn/project=OpenSaola

# Check a package Secret's labels and annotations
kubectl get secret <name> -n <data-namespace> -o jsonpath='{.metadata.labels}'
kubectl get secret <name> -n <data-namespace> -o jsonpath='{.metadata.annotations}'

# List all baselines published by a specific package
kubectl get mb -l middleware.cn/packagename=<package-name>
kubectl get mob -l middleware.cn/packagename=<package-name>
kubectl get mab -l middleware.cn/packagename=<package-name>
```

---

## Checking Conditions

Conditions provide the most detailed diagnostic information for any OpenSaola resource. Each condition represents a step in the reconcile flow.

### Reading conditions

```bash
# Get all conditions as JSON
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.'

# Get only failed conditions
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.status == "False")'

# Get a specific condition type
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.type == "Checked")'
```

### Condition types and what they mean

| Condition Type | Applies to | What it checks |
|---------------|------------|----------------|
| `Checked` | Middleware, MO, Action | Spec validation passed |
| `TemplateParseWithBaseline` | Middleware, MO | Baseline merge and template rendering succeeded |
| `BuildExtraResource` | Middleware, MO | Extra resources (from Configurations) were created |
| `ApplyRBAC` | MO | RBAC resources (ServiceAccount, Role/ClusterRole, Bindings) were applied |
| `ApplyOperator` | MO | Operator Deployment was applied |
| `ApplyCluster` | Middleware | CustomResource was created/updated |
| `MapCueFields` | Action | CUE field mapping succeeded |
| `ExecuteAction` | Action | Action execution completed |
| `ExecuteCue` | Action | CUE step executed |
| `ExecuteCmd` | Action | Command step executed |
| `ExecuteHttp` | Action | HTTP step executed |
| `Running` | Middleware, MO | Resource is running normally |
| `Updating` | Middleware, MO | Upgrade flow status |

### Condition status values

- `True` -- The step completed successfully
- `False` -- The step failed (check `message` for details)
- `Unknown` -- The step is initializing (reason: `Initing`)

---

## Log Configuration

OpenSaola's logging can be configured via Helm values or by editing the operator's ConfigMap.

### Helm values

```yaml
config:
  # Log level: 0=debug, 1=info, 2=warn, 3=error
  logLevel: 0
  # Log format: "console" (human-readable) or "json" (structured)
  logFormat: "console"
  # Log file path (empty string disables file logging)
  logFilePath: ""
```

### Changing log level at runtime

Update the ConfigMap and restart the operator pod:

```bash
# Edit the operator ConfigMap
kubectl edit configmap opensaola-config -n <operator-namespace>

# Restart the operator to pick up changes
kubectl rollout restart deployment opensaola -n <operator-namespace>
```

### Recommended log levels for troubleshooting

- **Level 0 (debug)**: Maximum verbosity. Use when investigating reconcile flow issues. Shows all condition transitions, merge operations, and template rendering details.
- **Level 1 (info)**: Default level. Shows reconcile events, state transitions, and key operations.
- **Level 2 (warn)**: Shows only warnings and errors. Use in production when the system is stable.
- **Level 3 (error)**: Shows only errors. Use when you want to filter out noise and focus on failures.

---

## Further Reading

- [Technical Documentation](opensaola-technical.md) -- Architecture, CRD field reference, reconcile flows, state machine details, labels/annotations conventions
- [Package Documentation](opensaola-packaging.md) -- Package format, baseline system, action system, configuration templates, Redis case study
