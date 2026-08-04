**English** | [中文](reconcile-suspension_zh.md)

# Reconciliation Suspension and Recovery Runbook

This runbook explains how to temporarily suspend child-resource writes for a Middleware (MID) or MiddlewareOperator (MO), then resume reconciliation safely. It applies to maintenance, manual intervention, incident handling, migration/cutover, and disaster recovery; disaster recovery is only one use case.

## Scope and guarantees

- `merge` is available only for a MID and automatically adopts non-conflicting changes made to its **primary live CR `spec`** while reconciliation is paused.
- **PreAction boundary:** the pause-snapshot and B/L/D merge renderer replays CUE-only PreActions on an in-memory MID copy, so their primary-CR `spec` effects match a normal primary-CR write without executing commands, HTTP actions, or Kubernetes writes. If any PreAction contains a non-CUE step (for example CMD or HTTP), `merge` fails closed. Make the action pure CUE, make its result an explicit MID/Baseline desired value, or use `apply` only when intentionally overwriting changes made during the pause.
- It does not adopt changes to CR metadata, Configuration resources, PreActions, or MiddlewareOperator resources. Update their desired configuration separately.
- The controller persists its pause snapshot in `.status.reconcilePause`; no in-process cache is required.
- Snapshot and live primary-CR `spec` input are limited to 256 KiB. Arrays are compared as whole values, and an explicit JSON `null` is not automatically adopted. Both cases fail closed.
- If the primary CR is deleted/recreated, or its GVK, name, or namespace changes, recovery fails closed rather than applying a patch to a different object.

## MID: pause, modify, and merge

### 1. Pause child-resource writes

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/suspend-reconcile=true --overwrite
```

The controller continues finalizer cleanup and runtime status synchronization, but stops the MID's primary-CR writes, Configuration writes, and deleted-CR self-healing.

### 2. Verify that the pause is safely active

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq '{
  pause: .status.reconcilePause,
  conditions: [.status.conditions[] | select(.type == "ReconcilePaused" or .type == "ReconcileAdoption")]
}'
```

Before changing the live primary CR, confirm all of the following:

1. `ReconcilePaused=True`.
2. `.status.reconcilePause` exists and has `desiredSpec`.
3. `ReconcileAdoption` is absent or not `False`.

If snapshot capture failed, do not change the live primary CR. Fix the missing/invalid primary CR, rendering failure, or size limit first, then recheck the MID.

### 3. Make a primary-CR change that should persist

Modify only the primary CR's `spec`, for example with `kubectl edit <primary-kind> <primary-name> -n <namespace>`. Keep a record of the intentional change. Do not rely on this workflow for CR metadata or secondary resources.

### 4. Resume with `merge` (when every PreAction step is CUE-only)

Before requesting `merge`, verify that every configured PreAction step is CUE-only. CUE PreActions may modify the primary CR `spec`; their patches are replayed in memory. If an action includes CMD, HTTP, or another non-CUE step, the controller safely rejects `merge`; use the alternatives in [Scope and guarantees](#scope-and-guarantees).

Keep the pause annotation and request the recovery policy:

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=merge --overwrite
```

The controller compares pause-time desired state (B), the live primary-CR `spec` (L), and current rendered desired state (D). Non-conflicting changes from L are stored in `spec.reconcileOverrides`; immediately before the following primary-CR write, the controller checks that L did not change again. If an external system or a human changes it again, the controller merges again instead of writing an older value.

### 5. Verify completion

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq '{
  suspend: .metadata.annotations["middleware.cn/suspend-reconcile"],
  resumePolicy: .metadata.annotations["middleware.cn/resume-policy"],
  reconcilePause: .status.reconcilePause,
  conditions: [.status.conditions[] | select(.type == "ReconcilePaused" or .type == "ReconcileAdoption")],
  reconcileOverrides: .spec.reconcileOverrides
}'
```

A successful resume removes the pause snapshot and both recovery annotations. `spec.reconcileOverrides` may remain; it preserves adopted changes from the pause until an explicit desired-state change at the same path supersedes them.

## MID: intentionally discard changes from the pause

Use `apply` only when it is correct to replay the current rendered desired state and discard live primary-CR changes made during the pause:

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=apply --overwrite
```

Do not remove `suspend-reconcile` directly. Without a valid recovery policy, the MID remains write-protected to prevent the old forced-replay behavior.

## Conflict or adoption failure

If `ReconcileAdoption=False`, inspect its `reason` and `message`:

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq \
  '.status.conditions[] | select(.type == "ReconcileAdoption")'
```

- `ReconcileAdoptionConflict`: a change from the pause and current desired state changed the same path. Resolve the intended value in the primary CR or in MID/Baseline desired state, then request `resume-policy=merge` again.
- `ReconcileSnapshotFailed` or another adoption failure: keep the pause in place and fix the reported identity, render, size, or JSON-`null` issue before retrying.

Never bypass a failed merge by directly removing the pause annotation. Use `apply` only after explicitly accepting that changes from the pause will be overwritten.

## MiddlewareOperator (MO) has ordinary pause/resume semantics

An MO pause protects its Configuration, RBAC, Deployment creation/update, and Deployment drift repair. It does not use B/L/D merging or `resume-policy`.

```bash
kubectl annotate mo <mo-name> -n <namespace> \
  middleware.cn/suspend-reconcile=true --overwrite

# Confirm ReconcilePaused=True before making a change during the pause.
kubectl get mo <mo-name> -n <namespace> -o json | jq \
  '.status.conditions[] | select(.type == "ReconcilePaused")'

# Resume ordinary desired-state reconciliation.
kubectl annotate mo <mo-name> -n <namespace> \
  middleware.cn/suspend-reconcile-
```

Before resuming an MO, update its desired configuration if a change made during the pause must persist. Any related MID is independent and must be paused and resumed through the MID workflow above.

## Related documentation

- [Technical Documentation](opensaola-technical_en.md)
- [Troubleshooting Guide](troubleshooting.md)
