# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Reconciliation suspension and explicit MID recovery policies: `merge` replays CUE-only PreActions on an in-memory MID copy and adopts non-conflicting primary-CR `spec` changes made during a pause, while `apply` intentionally replays desired state.
- Durable pause snapshots, `reconcileOverrides`, and reconciliation Conditions that fail closed on unsafe recovery.
- Kind-aware Configuration disable lifecycle inventory: PVC/PV default to `orphan`, CRDs are hard-protected, and every other Kind defaults to `delete`; cleanup still requires a previously recorded, still-owned resource.
- Reconciliation suspension and recovery runbooks in English and Chinese.

### Known limitations
- `resume-policy=merge` fails closed when any PreAction contains a non-CUE step (for example CMD or HTTP), because pause rendering must not execute external side effects. Make the action pure CUE, make its result explicit desired state, or use `apply` only when intentionally overwriting a change made during the pause.

## [1.5.0] - 2025-01-01

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
