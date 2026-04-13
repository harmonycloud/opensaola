**English** | [中文](upgrade-guide_zh.md)

# Upgrade Guide

This guide covers upgrading the OpenSaola operator and middleware packages. For detailed architecture information, see the [Technical Documentation](opensaola-technical.md).

## Table of Contents

- [Pre-upgrade Checklist](#pre-upgrade-checklist)
- [Upgrading the OpenSaola Operator](#upgrading-the-opensaola-operator)
- [Upgrading Middleware Packages](#upgrading-middleware-packages)
- [Post-upgrade Verification](#post-upgrade-verification)
- [Rollback Procedure](#rollback-procedure)
- [CRD Compatibility Notes](#crd-compatibility-notes)
- [Troubleshooting Upgrades](#troubleshooting-upgrades)

---

## Pre-upgrade Checklist

Before upgrading, complete the following steps:

### 1. Back up all Custom Resources

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

### 2. Check the current version

```bash
# Check the running operator version
helm list -n <operator-namespace>

# Check the operator pod image version
kubectl get deployment opensaola -n <operator-namespace> -o jsonpath='{.spec.template.spec.containers[0].image}'
```

### 3. Review the CHANGELOG for breaking changes

Check the project's release notes at [https://github.com/OpenSaola/opensaola/releases](https://github.com/OpenSaola/opensaola/releases) for:
- CRD field additions, removals, or renames
- Changes to label or annotation conventions
- Changes to the reconcile flow or state machine behavior
- Helm values changes

### 4. Ensure cluster has sufficient resources

```bash
# Check node resources
kubectl top nodes

# Check existing operator pod resource usage
kubectl top pod -l app.kubernetes.io/name=opensaola -n <operator-namespace>
```

### 5. Verify all middleware instances are healthy

```bash
# Check that all Middleware instances are Available
kubectl get mid -A

# Check that all MiddlewareOperators are Available
kubectl get mo -A

# Check that there are no resources in Updating state
kubectl get mid -A -o jsonpath='{range .items[?(@.status.state!="Available")]}{.metadata.namespace}/{.metadata.name}: {.status.state}{"\n"}{end}'
```

---

## Upgrading the OpenSaola Operator

### Step 1: Update the Helm repository

```bash
helm repo update
```

### Step 2: Review new values

```bash
# View the default values for the new version
helm show values opensaola/opensaola --version <new-version>

# Compare with your current values
helm get values opensaola -n <operator-namespace> > current-values.yaml
```

### Step 3: Perform the upgrade

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

### Step 4: Verify CRD updates

CRDs are managed via a pre-upgrade Helm hook (a `kubectl apply` Job). The hook runs before the main chart resources are upgraded.

```bash
# Verify CRDs are at the expected version
kubectl get crds | grep middleware.cn

# Check CRD details
kubectl get crd middlewares.middleware.cn -o jsonpath='{.metadata.resourceVersion}'
```

---

## Upgrading Middleware Packages

Middleware packages (e.g., Redis, MySQL) are upgraded independently of the OpenSaola operator. The upgrade is triggered by annotations on the Middleware and MiddlewareOperator resources.

### Step 1: Upload the new package version

Use `saola-cli` to upload the new package version:

```bash
saola package upload --path <package-directory>
```

This creates or updates a Secret in the data namespace with the new package content.

### Step 2: Verify the new package is published

```bash
# Check the new MiddlewarePackage
kubectl get mp -l middleware.cn/packageversion=<new-version>

# Verify baselines are created
kubectl get mb -l middleware.cn/packageversion=<new-version>
kubectl get mob -l middleware.cn/packageversion=<new-version>
```

### Step 3: Trigger the middleware upgrade

Set the `middleware.cn/update` and `middleware.cn/baseline` annotations to trigger the upgrade:

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

### Step 4: Monitor the upgrade

```bash
# Watch the state transition
kubectl get mid <name> -n <namespace> -w

# Check the Updating condition
kubectl get mid <name> -n <namespace> -o jsonpath='{.status.conditions}' | jq '.[] | select(.type == "Updating")'
```

The resource will transition: `Available` -> `Updating` -> `Available` (on success) or `Unavailable` (on failure).

---

## Post-upgrade Verification

After upgrading the operator or middleware packages, verify the system is healthy.

### Check operator pod status

```bash
# Verify the operator pod is running
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>

# Check for any restarts
kubectl describe pod -l app.kubernetes.io/name=opensaola -n <operator-namespace> | grep -A2 "Restart Count"

# Verify the new image is running
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace> -o jsonpath='{.items[0].spec.containers[0].image}'
```

### Verify CRD versions

```bash
# List all OpenSaola CRDs
kubectl get crds | grep middleware.cn

# Check stored versions
kubectl get crd middlewares.middleware.cn -o jsonpath='{.status.storedVersions}'
```

### Check middleware instance health

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

### Check operator logs for errors

```bash
kubectl logs -l app.kubernetes.io/name=opensaola -n <operator-namespace> --tail=200 | grep -i "error\|fail\|panic"
```

---

## Rollback Procedure

### Rolling back the operator

```bash
# View Helm release history
helm history opensaola -n <operator-namespace>

# Rollback to a previous revision
helm rollback opensaola <revision-number> -n <operator-namespace>

# Verify the rollback
helm list -n <operator-namespace>
kubectl get pods -l app.kubernetes.io/name=opensaola -n <operator-namespace>
```

### CRD rollback considerations

CRDs are **not** automatically rolled back by `helm rollback` because they are applied via a pre-upgrade hook Job. If the new CRD version is backward-compatible (i.e., only added new optional fields), no CRD rollback is needed. If the new CRD version introduced breaking changes:

1. Manually apply the old CRD definitions:
   ```bash
   kubectl apply -f <old-crd-definitions-directory>/
   ```
2. Verify existing resources are still valid against the old CRD schema

### Rolling back middleware packages

Middleware package upgrades can be rolled back by re-triggering an upgrade to the previous version:

```bash
# Set the update annotation to the previous version
kubectl annotate mid <name> -n <namespace> \
  middleware.cn/update=<previous-version> \
  middleware.cn/baseline=<previous-baseline-name> \
  --overwrite
```

Alternatively, if the upgrade annotations are still present and the upgrade has not completed, you can remove them to abort:

```bash
kubectl annotate mid <name> -n <namespace> middleware.cn/update- middleware.cn/baseline-
```

---

## CRD Compatibility Notes

### How CRDs are managed

- CRDs are installed and upgraded via a pre-install/pre-upgrade Helm hook
- The hook uses a `kubectl apply` Job to apply CRD manifests from the chart
- CRDs are **not deleted** when the Helm release is uninstalled (standard Helm behavior for CRDs)

### Backward compatibility rules

| Change type | Backward compatible? | Notes |
|------------|---------------------|-------|
| Adding new optional fields | Yes | Existing resources are unaffected |
| Adding new required fields | No | Existing resources will fail validation |
| Removing fields | No | Existing resources referencing removed fields will lose data |
| Renaming fields | No | Equivalent to removing + adding, requires migration |
| Changing field types | No | Existing data may become invalid |
| Adding new enum values | Yes | Existing resources are unaffected |
| Removing enum values | No | Existing resources using removed values will fail validation |

### Migration for breaking CRD changes

If a new version introduces breaking CRD changes, follow these steps:

1. Back up all affected resources (see [Pre-upgrade Checklist](#1-back-up-all-custom-resources))
2. Read the release notes for specific migration instructions
3. Apply the new CRDs
4. Migrate existing resources using `kubectl edit` or scripted `kubectl patch` commands
5. Upgrade the operator
6. Verify all resources reconcile successfully

---

## Troubleshooting Upgrades

For general troubleshooting, see the [Troubleshooting Guide](troubleshooting.md).

### Operator upgrade issues

| Symptom | Likely cause | Solution |
|---------|-------------|----------|
| New pod stuck in `CrashLoopBackOff` | Missing CRDs or incompatible config | Check pod logs with `--previous` flag |
| CRD hook Job failed | RBAC or network issues | Check Job logs: `kubectl logs job/opensaola-crd-install -n <ns>` |
| Existing resources become `Unavailable` after upgrade | Breaking CRD changes or reconcile logic changes | Check conditions, review release notes |

### Package upgrade issues

| Symptom | Likely cause | Solution |
|---------|-------------|----------|
| Middleware stuck in `Updating` | New baseline not found or template error | Check `Updating` condition message |
| Upgrade annotation ignored | Resource already in `Updating` state from a previous attempt | Check and resolve the current state first |
| New baseline not created | Package Secret missing labels or corrupted | Re-upload with `saola-cli` |

### Further reading

- [Troubleshooting Guide](troubleshooting.md) -- Detailed debugging for common issues
- [Technical Documentation](opensaola-technical.md) -- Upgrade trigger mechanism (Section 8), deletion flow (Section 9)
- [Package Documentation](opensaola-packaging.md) -- Package format and baseline system
