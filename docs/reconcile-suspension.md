**English** | [中文](reconcile-suspension_zh.md)

# Reconciliation Suspension and Recovery Runbook

This runbook explains how to temporarily suspend child-resource writes for a Middleware (MID) or MiddlewareOperator (MO), then resume reconciliation safely. It applies to maintenance, manual intervention, incident handling, migration/cutover, and disaster recovery; disaster recovery is only one use case.

## Scope and guarantees

- `merge` is available only for a MID and automatically adopts changes made to its **primary live CR `spec`** while reconciliation is paused.
- **PreAction boundary:** pause capture reads the actual CR without rendering or executing PreActions. During `merge`, the desired-state renderer replays CUE PreActions on an in-memory MID copy, matching their primary-CR `spec` effects during a normal write; every step must have nonempty CUE. A step without CUE that requires CMD, HTTP, or another non-CUE operation makes `merge` fail closed. Rendering never executes those external operations or Kubernetes writes. Make the action pure CUE, make its result an explicit MID/Baseline desired value, or use `apply` only when intentionally overwriting changes made during the pause.
- It does not adopt changes to CR metadata, Configuration resources, PreActions, or MiddlewareOperator resources. Update their desired configuration separately.
- The actual pause-start spec and previous overrides are stored in an immutable Secret in the MID namespace, using the existing fast zstd codec (JSON when compression is not smaller). `.status.reconcilePause` contains only the `snapshotSecret` reference, target identity, hashes and timestamps; it does not duplicate the payload or depend on process memory.
- No application snapshot size ceiling is imposed; Kubernetes validates the stored Secret and resource sizes. Pause captures the actual spec with GET. Resume compares that observation with the current actual spec. Unchanged defaults, nulls and pre-existing drift are not adopted. Associative lists use API schema keys, sets use membership, and schema-declared atomic values are handled as a whole.
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
2. `.status.reconcilePause.snapshotSecret` refers to a valid Secret in the same namespace.
3. `ReconcileAdoption` is absent or not `False`.

If snapshot capture failed, do not change the live primary CR. Fix the missing primary CR, target-resolution failure, Secret write failure, or Kubernetes size-limit issue first, then recheck the MID.

### 3. Make a primary-CR change that should persist

Modify only the primary CR's `spec`, for example with `kubectl edit <primary-kind> <primary-name> -n <namespace>`. Keep a record of the intentional change. Do not rely on this workflow for CR metadata or secondary resources.

### 4. Resume with `merge` (when every PreAction step is CUE-only)

Before requesting `merge`, verify that every configured PreAction step has nonempty CUE. CUE PreActions may modify the primary CR `spec`; their patches are replayed in memory. A step without CUE that requires CMD, HTTP, or another non-CUE operation makes the controller reject `merge`; use the alternatives in [Scope and guarantees](#scope-and-guarantees).

Keep the pause annotation and request the recovery policy:

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=merge --overwrite
```

The controller compares the actual spec captured at pause start with the actual spec before resume. It applies only the additions, modifications and deletions from that interval to current rendered desired state, then persists `spec.reconcileOverrides`. At observed changed paths the current CR value wins; unrelated MID desired inputs remain. Subsequent explicit MID/Baseline changes still take precedence under the existing override rules.

Before writing, the actual spec is checked again. A further change triggers recomputation. The snapshot Secret retains `previousOverrides` from before the pause so a retry cannot retain a reverted intermediate adoption.

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

A successful resume clears the status reference and both recovery annotations, then deletes the pause Secret. Failed deletion or interrupted cleanup is retried by the existing collector (a 10-minute scan and 30-minute minimum age by default). Active pause references never expire. Deleting the MID also garbage-collects its Secrets through controller ownerReferences. Missing, foreign or corrupt Secrets keep merge protection active and never trigger a new baseline capture during resume. `spec.reconcileOverrides` may remain; it preserves adopted changes from the pause until an explicit desired-state change at the same path supersedes them.

## MID: intentionally discard changes from the pause

Use `apply` only when it is correct to replay the current rendered desired state and discard live primary-CR changes made during the pause:

```bash
kubectl annotate mid <mid-name> -n <namespace> \
  middleware.cn/resume-policy=apply --overwrite
```

Do not remove `suspend-reconcile` directly. Without a valid recovery policy, the MID remains write-protected to prevent the old forced-replay behavior.

## Adoption failure

If `ReconcileAdoption=False`, inspect its `reason` and `message`:

```bash
kubectl get mid <mid-name> -n <namespace> -o json | jq \
  '.status.conditions[] | select(.type == "ReconcileAdoption")'
```

- `ReconcileSnapshotFailed` or another adoption failure: keep the pause in place and fix the reported identity, schema, render, or size issue before retrying.

A legacy `desiredSpec` snapshot is not an actual baseline and is never silently recaptured. Finish paused sessions before upgrading. If only a legacy snapshot remains after upgrading, keep the pause, explicitly preserve required actual changes in desired inputs, then select `apply`.

Never bypass a failed merge by directly removing the pause annotation. Use `apply` only after explicitly accepting that changes from the pause will be overwritten.

## MiddlewareOperator (MO) has ordinary pause/resume semantics

An MO pause protects its Configuration, RBAC, Deployment creation/update, and Deployment drift repair. It does not use primary-CR change adoption or `resume-policy`.

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

## Comparison with the historical implementation

Commit `78adfe0`, dated 2026-10-14 and included in `v1.4.0`, switched recovery to actual-state snapshots and adoption of changes made during the pause. The historical implementation below is its parent commit, `d97a165`. Use the preceding procedures for current operations.

| Decision | Historical implementation | Current implementation |
|----------|---------------------------|------------------------|
| Pause baseline | Desired `spec` rendered at pause start | Actual CR `spec` read at pause start |
| Storage and confirmation | `desiredSpec` inside MID status | `.status.reconcilePause.snapshotSecret` refers to a valid immutable Secret in the same namespace; the Secret holds the actual spec and pre-pause overrides |
| Merge rule | B is pause-time desired state, L is actual state before resume, and D is current desired state; use D when L=B, use L when D=B, accept a shared L=D value, otherwise report a conflict at the changed path | Compare actual specs at pause start and before resume, then apply only that interval's additions, modifications, and deletions to current desired state |
| Both sides change the same path | Different final values that both differ from B produce `ReconcileAdoptionConflict` and keep the pause active | The actual change made during the pause wins for this `merge`; unrelated current desired inputs remain |
| Pre-existing defaults and drift | Differences between actual and rendered desired state could be adopted | Values unchanged during the pause are not adopted |
| Lists and nulls | Arrays are atomic; an observed actual null is treated as the baseline value | Lists follow API schema semantics; unchanged nulls are not adopted, and actual changes are compared using the schema |
| PreActions | Both capture and merge render desired state and require render-safe CUE steps | Capture does not render; merge performs in-memory CUE rendering |
| Size limits | A 256 KiB application limit on the desired snapshot, actual spec before resume, and rendered base of a nonempty override | No such application limits; Kubernetes validates resource sizes |
| Merge retries | Current MID overrides could include an intermediate adoption | Recompute from snapshot `previousOverrides`, excluding reverted intermediate changes |

For example, actual and desired replicas both start at 3. During the pause, the actual CR changes to 5 and MID desired state changes to 4. The historical implementation reports a conflict; current `merge` produces 5. A subsequent explicit MID desired change from 4 to 6 takes effect as 6.

In another example, desired replicas are 3 but actual replicas have already drifted to 2 before the pause, with no changes during the pause. The historical implementation could adopt 2. The current implementation observes no interval change and uses the current desired value, 3.

Explicit MID `merge`/`apply` selection, write protection without a policy, actual-spec verification before writing, and independent MO pause/resume remain unchanged. Sessions with only a legacy `desiredSpec` snapshot must follow the upgrade handling in [Adoption failure](#adoption-failure); an old desired snapshot cannot be converted into an actual baseline.

## Related documentation

- [Technical Documentation](opensaola-technical_en.md)
- [Troubleshooting Guide](troubleshooting.md)
