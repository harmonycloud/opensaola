# Contributing to OpenSaola

Thank you for your interest in contributing to OpenSaola! This document provides guidelines, development workflows, and architectural context to help you get started quickly.

## Quick Start for Development

### Prerequisites

- **Go 1.26.1+** (see `go.mod`)
- **Docker** (or another OCI-compatible container tool)
- **kubectl**
- **Kind** (for local clusters and e2e tests)
- **Helm 3**

### Clone and Build

```bash
git clone https://github.com/OpenSaola/opensaola.git
cd opensaola
make build
```

This generates CRDs, runs code generation, formats, vets, and compiles the manager binary to `bin/manager`.

### Run Unit Tests

```bash
make test
```

> **Note:** `make test` excludes controller envtest tests (`internal/controller/`) and e2e tests (`test/e2e/`). It runs all other packages with coverage.

### Run Tests with Race Detector

```bash
make test-race
```

Same scope as `make test`, but enables Go's race detector and disables test caching (`-count=1`).

### Run Integration Tests (envtest)

Controller tests use [envtest](https://book.kubebuilder.io/reference/envtest.html) to spin up a lightweight K8s API server in-process.

```bash
make setup-envtest    # downloads K8s API server binaries
make test-envtest     # runs controller tests with envtest
```

### Run Locally Against a Kind Cluster

```bash
make kind-create      # create a Kind cluster (default name: opensaola-e2e)
make install          # install CRDs into the cluster
make run              # run the controller locally against the cluster
```

### Run E2E Tests

**Option A -- Smoke test (creates and tears down a temporary Kind cluster):**

```bash
make docker-build IMG=controller:latest
kind load docker-image controller:latest --name opensaola-e2e
make test-e2e-smoke
```

**Option B -- Against an existing Kind cluster:**

```bash
make kind-create
make docker-build IMG=controller:latest
kind load docker-image controller:latest --name opensaola-e2e
make test-e2e
```

## Project Architecture

OpenSaola is a Kubernetes operator that manages middleware lifecycle. It follows a three-layer architecture.

### Controller Layer (`internal/controller/`)

Each CRD has a dedicated controller file. Controllers handle:

- Reconcile loop entry point
- Finalizer management
- Status convergence
- Predicate-based event filtering
- Metrics collection

Controllers in this project:

| Controller | CRD |
|---|---|
| `middleware_controller.go` | Middleware |
| `middlewareoperator_controller.go` | MiddlewareOperator |
| `middlewareoperator_runtime_controller.go` | MiddlewareOperator (runtime reconciliation) |
| `middlewareaction_controller.go` | MiddlewareAction |
| `middlewarepackage_controller.go` | MiddlewarePackage |
| `middlewarebaseline_controller.go` | MiddlewareBaseline |
| `middlewareoperatorbaseline_controller.go` | MiddlewareOperatorBaseline |
| `middlewareactionbaseline_controller.go` | MiddlewareActionBaseline |
| `middlewareconfiguration_controller.go` | MiddlewareConfiguration |

### Service Layer (`internal/service/`)

Business logic for each CRD type, organized by subdirectory:

- **middleware/** -- Middleware instance lifecycle (install, update, delete, upgrade)
- **middlewareoperator/** -- Operator deployment management
- **middlewareaction/** -- Action execution (CUE, HTTP, CMD)
- **middlewarepackage/** -- Package resource reconciliation
- **packages/** -- Package TAR parsing and caching
- **middlewarebaseline/** -- Baseline template management
- **middlewareoperatorbaseline/** -- Operator baseline management
- **middlewareactionbaseline/** -- Action baseline management
- **middlewareconfiguration/** -- Configuration resource handling
- **synchronizer/** -- State synchronization between resources
- **watcher/** -- Custom resource watching and event handling
- **customresource/** -- Shared custom resource utilities
- **status/** -- Status and condition helpers
- **consts/** -- Shared constants

### K8s Client Layer (`internal/k8s/`)

Kubernetes API wrappers for all resource types. This layer provides typed accessors with caching for Deployments, StatefulSets, DaemonSets, Secrets, Services, PVCs, Pods, RBAC resources, and all OpenSaola custom resources.

### CRD Types (`api/v1/`)

All CRD type definitions live here. Key files:

- `middleware_types.go`, `middlewareoperator_types.go`, `middlewareaction_types.go`
- `middlewarepackage_types.go`, `middlewarebaseline_types.go`
- `middlewareoperatorbaseline_types.go`, `middlewareactionbaseline_types.go`
- `middlewareconfiguration_types.go`
- `labels.go` -- shared label keys
- `common.go` -- shared type helpers

## Available Make Targets

### General

| Target | Description |
|---|---|
| `make help` | Display all targets with descriptions |

### Development

| Target | Description |
|---|---|
| `make manifests` | Generate CRD manifests, RBAC, and webhook configurations |
| `make generate` | Generate DeepCopy method implementations |
| `make fmt` | Run `go fmt` against all code |
| `make vet` | Run `go vet` against all code |
| `make test` | Run unit tests (excludes controller envtest and e2e) |
| `make test-race` | Run unit tests with race detector |
| `make test-envtest` | Run envtest integration tests for controllers |
| `make bench` | Run Go benchmarks |
| `make benchstat` | Compare two benchmark outputs (requires `BENCH_OLD` and `BENCH_NEW`) |
| `make test-e2e` | Run e2e tests against an existing Kind cluster |
| `make test-e2e-smoke` | Run e2e tests on a temporary Kind cluster (auto-creates and deletes) |
| `make kind-create` | Create a Kind cluster (default name: `opensaola-e2e`) |
| `make kind-delete` | Delete the Kind cluster |
| `make lint` | Run golangci-lint |
| `make lint-fix` | Run golangci-lint with auto-fix |
| `make lint-config` | Verify golangci-lint configuration |

### Build

| Target | Description |
|---|---|
| `make build` | Build the manager binary to `bin/manager` |
| `make run` | Run the controller locally from source |
| `make docker-build` | Build the Docker image (default tag: `controller:latest`) |
| `make docker-push` | Push the Docker image |
| `make docker-buildx` | Build and push multi-arch Docker image |
| `make build-installer` | Generate a consolidated install YAML in `dist/install.yaml` |

### Deployment

| Target | Description |
|---|---|
| `make install` | Install CRDs into the current cluster |
| `make uninstall` | Uninstall CRDs from the current cluster |
| `make deploy` | Deploy the controller to the current cluster |
| `make deploy-e2e` | Deploy the controller with e2e overlay (all feature gates enabled) |
| `make undeploy` | Remove the controller from the current cluster |
| `make undeploy-e2e` | Remove the controller (e2e overlay) from the current cluster |

### Dependencies

| Target | Description |
|---|---|
| `make kustomize` | Download kustomize locally |
| `make controller-gen` | Download controller-gen locally |
| `make setup-envtest` | Download envtest K8s API server binaries |
| `make envtest` | Download setup-envtest tool |
| `make golangci-lint` | Download golangci-lint locally |
| `make benchstat-bin` | Download benchstat locally |

All dependency binaries are installed to `./bin/` within the project.

## Feature Gates

OpenSaola supports feature gates for gradual rollout of new behaviors. Feature gates are controlled via environment variables set on the manager container. Set any gate to `"true"` to enable it.

| Environment Variable | Description |
|---|---|
| `FG_FINALIZERS` | Enable finalizer-based resource cleanup during deletion |
| `FG_REPLACEPACKAGE_REQUEUE` | Requeue reconciliation when a package is replaced mid-flight |
| `FG_MO_FILTER_DEPLOYMENT_EVENTS` | Filter out unnecessary Deployment events in MiddlewareOperator controller |
| `FG_MO_FILTER_STATUS_EVENTS` | Filter out redundant status-only updates in MiddlewareOperator controller |
| `FG_MP_SECRET_ENQUEUE` | Enqueue MiddlewarePackage reconciliation on Secret changes |
| `FG_CONTROLLER_CONCURRENCY_LIMITS` | Enable per-controller concurrency limits for reconcile workers |
| `FG_MA_STATUS_DEDUP` | Deduplicate status updates in MiddlewareAction controller |
| `FG_METRICS_RECONCILE_STEPS` | Emit per-step metrics during reconciliation |
| `FG_METRICS_RECONCILE_TOTAL` | Emit total reconciliation duration metrics |
| `FG_PACKAGE_CACHE` | Enable in-memory caching for parsed package contents |

All feature gates are enabled in the e2e test overlay (`config/e2e/manager_featuregates_patch.yaml`). For production, enable gates selectively based on your needs.

## Code Style

- Run `go fmt ./...` and `go vet ./...` before committing. `make lint` runs the full golangci-lint suite.
- Use **structured logging** via `log.FromContext(ctx)`. Do not use `fmt.Print` or the standard `log` package.
- Error wrapping format: `fmt.Errorf("failed to <verb> <noun>: %w", err)`.
- Keep function signatures consistent with existing patterns in the same package.
- Prefer table-driven tests.
- CRD type changes require running `make manifests generate` to regenerate derived files.

## Commit Messages

Use conventional commit format:

- `feat`: new feature
- `fix`: bug fix
- `docs`: documentation changes
- `refactor`: code refactoring (no behavior change)
- `test`: adding or updating tests
- `chore`: maintenance, dependency updates

Examples:

```
feat: add package cache feature gate
fix: prevent duplicate status updates in MiddlewareAction controller
test: add envtest coverage for finalizer cleanup
```

## Pull Request Process

1. **Fork and branch** -- Create a feature branch from `main`.
2. **Make changes** -- Keep commits focused and atomic.
3. **Regenerate if needed** -- If you modified CRD types in `api/v1/`, run:
   ```bash
   make manifests generate
   ```
   Commit the generated changes alongside your source changes.
4. **Run tests** -- At minimum, run:
   ```bash
   make test           # unit tests
   make lint           # linter
   ```
   If you changed controller logic, also run:
   ```bash
   make test-envtest   # controller integration tests
   ```
5. **Update documentation** -- Update relevant docs if behavior changes.
6. **Open the PR** -- Provide a clear description of what changed and why.
7. **Request review** -- Tag appropriate reviewers.

## Running Specific Tests

```bash
# Run a specific test by name
go test ./internal/service/packages/... -v -run TestGetPackage

# Run controller envtest tests
go test ./internal/controller/... -tags=envtest -v

# Run benchmarks for a specific package
go test ./internal/service/packages/... -run '^$' -bench . -benchmem

# Run tests with verbose output and no caching
go test ./internal/service/middleware/... -v -count=1
```

## Reporting Issues

Use GitHub Issues with appropriate labels. When reporting a bug, include:

- OpenSaola version or commit hash
- Kubernetes version
- Steps to reproduce
- Expected vs. actual behavior
- Relevant logs (controller logs, `kubectl describe` output)

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
