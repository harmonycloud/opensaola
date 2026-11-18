# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.5.1] - 2026-11-18

### Fixed
- Restore first publication of StatefulSets with PVC templates. The controller accepts API defaults inside atomic maps and lists while checking declared values, list membership and order. A 5Gi claim still fails verification if admission returns 1Gi. The same comparison covers projected volumes, NetworkPolicy ports, RBAC subjects, admission rules and CRD-declared defaults.
- Honor Configuration orphan policies when a Middleware or MiddlewareOperator is deleted. The controller detaches obsolete instance owner references from retained resources, including those of paused instances, without replaying their contents. Disabling a Configuration still follows its separate disable policy.
- Retain a separate Helm-managed data namespace and its catalog Secrets when changing the namespace setting or uninstalling the protected chart revision.

### Upgrade notes
- If the previous chart created the data namespace, first upgrade with the original `config.dataNamespace` and `config.createDataNamespace=true`. Once that upgrade succeeds, the release manifest records the keep policy. A subsequent upgrade can change the name or turn creation off. Helm needs permission to list namespaces during upgrade.
- Upgrade the bundled CRDs with the controller. Allow existing instances to reconcile before deleting their owners so the controller can remove legacy references. Kubernetes can already have collected dependents if background deletion started before the upgrade.

## [1.5.0] - 2026-11-18

### Changed
- Normal managed-resource updates enforce every currently declared field, while using history only for safe removals. Schema-free object lists with unique names merge recursively by name; declared schema map/set/atomic boundaries remain authoritative.
- Shared mutable resources use create-only POST and a single field plan for existing-resource writes, previews, and verification. One stable per-target Secret holds confirmed history and at most one pending operation. One-off Action edits use guarded JSON Patch.
- Existing v1 intent records migrate to v2 during normal updates without CLI migration. API defaults remain deletion evidence rather than authoritative user declarations.

### Fixed
- Removing a named member recursively removes recorded children while retaining external children and identity keys. Conflicting historical removals now block the complete target write instead of reporting success with stale fields.
- Validate planned removals against dry-run and commit responses. Pending deletion records retain the previous successful history until confirmation, including retries after lost responses, missing target pointers, and restarts.
- Classify intent deletion conflicts and incomplete field writes before generic keyword matching, so field paths containing `template` or `tls` do not mislabel their failure cause.
- Accept native CRD omitempty normalization such as omitted `preserveUnknownFields: false` during first installation, without relaxing unknown custom-resource field validation.
- Validate declared fields against both dry-run and committed API responses; incomplete writes report retryable errors instead of silently succeeding.
- Recreate deleted primary resources without replaying Configuration or side-effecting PreActions, with create-only publication, same-generation retries, and an existence-check fallback. Pause recovery continues to use actual before/after state.
- Preserve the target UID when registering Configuration cleanup, and retain verified same-owner history if the owner annotation is missing. A recovered create response can establish history only for explicitly declared fields.
- Honor mountPath for native volume mounts, preserve declared init-container order, and support withdrawal of native probe defaults. Preserve int64 precision across rendering, action steps, and pause recovery.

### Upgrade notes and limitations
- v1.4.0 cannot read new v2 ordinary intent records; rolling back the binary alone does not preserve normal updates for migrated resources.
- Older controllers cannot write the new mutable history Secret. Deploy one active writer version at a time.
- First migration retains uncertain removals inside formerly atomic opaque lists. Explicit null and schema-atomic values remain whole-value declarations; unknown mutually exclusive CR fields still require accurate schema or business validation.

## [1.4.0] - 2026-10-24

### Added
- Ordered CEL `statusRules` on OperatorBaseline GVKs project a managed custom resource's phase, reason, and replica count into Middleware status.
- Immutable, compressed Secret records of rendered resource intent support three-way updates of MID primary resources and MID/MO Configuration resources, including schema-aware field removal and protection of externally changed fields.
- Guarded adoption of legacy Configuration resources verifies their logical source and live ownership before atomically publishing changes and establishing lifecycle inventory.

### Changed
- Existing owned resources establish intent records on their next normal update. Startup does not bulk-rewrite unchanged resources, replay PreActions, or require the unreleased CLI SSA migration tooling.
- Reconciliation pause snapshots store the actual primary-resource spec and previous overrides in an immutable Secret. Recovery adopts only changes made during the pause and retries from the original snapshot.
- Configuration disable/delete handling honors resource-level policies, keeps orphan resources outside owner garbage collection, and respects explicit cross-namespace template targets.
- Official images embed Saola CLI v1.1.0, pinned to its released commit through the stable lock.

### Fixed
- Accept empty registry and repository image prefixes in the Helm chart (issue [#3](https://github.com/harmonycloud/opensaola/issues/3)).
- Skip MiddlewareOperator Deployment reconciliation while the owner is being deleted.
- Resolve Configuration deletion scope through the supplied client's RESTMapper, matching the apply path and avoiding implicit kubeconfig access in isolated tests.
- Pin CLI artifact rebuilds to the exact Go version declared in the released CLI source, preventing hosted-runner compiler changes from breaking checksum verification.

### Upgrade notes and limitations
- Finish existing reconciliation pause sessions before upgrading. Legacy `desiredSpec` snapshots are not automatically converted into actual-state snapshots.
- `resume-policy=merge` still requires CUE-only PreActions; use `apply` only when intentionally discarding changes made during a pause.
- Fields omitted before the first intent record are not inferred as safe to delete. Unknown or conflicting live fields remain protected and are reported.
- Rolling back the controller cannot restore fields already deleted by an explicit configuration change. Restore required values through the desired configuration and verify each affected instance.

## [1.3.0] - 2026-08-12

### Added
- Reconciliation suspension and explicit MID recovery policies: `merge` replays CUE-only PreActions on an in-memory MID copy and adopts non-conflicting primary-CR `spec` changes made during a pause, while `apply` intentionally replays desired state.
- Durable pause snapshots, `reconcileOverrides`, and reconciliation Conditions that fail closed on unsafe recovery.
- Kind-aware Configuration disable lifecycle inventory: PVC/PV default to `orphan`, CRDs are hard-protected, and every other Kind defaults to `delete`; cleanup still requires a previously recorded, still-owned resource.
- English and Chinese runbooks for reconciliation suspension and recovery.
- EMQX v2beta1 status projection, including lifecycle phase, reason, and combined core/replicant replica counts.

### Changed
- Custom-resource watcher and status-synchronization sessions now use resource-scoped ownership and self-healing restart behavior across retries and leader handovers.

### Fixed
- Restored custom-resource status synchronization after watcher recovery, without allowing one Middleware cleanup to stop another resource sharing the same GVK and namespace.
- Prevented a typed-nil panic while applying Configuration lifecycle management.
- Allow template rendering to proceed with empty Kubernetes capabilities when discovery or the server version is unavailable, such as in unit tests or CI without a kubeconfig.
- Treat observed live `spec` fields serialized as JSON `null` as unchanged during paused-reconciliation merges, preventing unintended override deletions.

### Known limitations
- `resume-policy=merge` fails closed when any PreAction contains a non-CUE step (for example CMD or HTTP), because pause rendering must not execute external side effects. Make the action pure CUE, make its result explicit desired state, or use `apply` only when intentionally overwriting a change made during the pause.

## Historical development notes

### Added
- Apache License 2.0 and license headers on all source files
- README with project overview, architecture, CRD reference, and quick start guide
- Community files: CONTRIBUTING.md, CODE_OF_CONDUCT.md, SECURITY.md
- GitHub Actions CI/CD workflows (lint, test, build, Docker multi-arch)
- Issue and PR templates
- Helm chart ServiceAccount and scoped RBAC
- Generic cache package (internal/cache) with TTL support
- Reconcile ID for end-to-end request tracing
- State transition logging for Middleware and MiddlewareOperator
- Configurable log format (JSON/console) via config
- Troubleshooting and upgrade documentation

### Changed
- Migrated internal packages from pkg/ to internal/ (k8s, service, resource, concurrency)
- Unified logging from global zerolog singleton to log.FromContext(ctx) (logr)
- Upgraded Kubernetes dependencies to v0.35.0 (controller-runtime v0.23.3)
- Replaced sync.Map caches with type-safe generic cache.Store
- Improved all error messages with actionable guidance
- Helm values.yaml now exposes all operator configuration
- Dockerfile uses public image sources and Alpine 3.20

### Fixed
- Helm RBAC: replaced cluster-admin wildcard with scoped permissions
- Dockerfile: removed internal IP references
- Spelling errors: Midddleware, tempalteValues, Faild
- Unsafe type assertions that could cause panics
- Silent error swallowing (12 instances now log warnings)
- Panic recovery pattern (uses logger instead of fmt.Printf)
- Build constraint position in envtest test files
- Cache API mismatch in test files after migration

### Removed
- github.com/pkg/errors dependency (replaced with stdlib fmt.Errorf %w)
- Dead code: ~120 lines of commented-out functions
- Internal IP addresses and company-specific references
- Chinese-only comments and log messages (translated to English)
- hostPath kubectl mount from Helm deployment
- Unused zap.Options code from cmd/main.go
