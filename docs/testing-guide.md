# OpenSaola Testing Guide

> Chinese version: [testing-guide_zh.md](testing-guide_zh.md)

This guide covers how to run every level of tests in the OpenSaola project, from fast unit tests to full end-to-end validation on a Kind cluster.

## Test Tiers Overview

| Tier | Command | Scope | Dependencies |
|------|---------|-------|--------------|
| Unit | `make test` | All packages except controller + e2e | None |
| Race | `make test-race` | Same as unit, with race detector | None |
| Envtest | `make test-envtest` | Controller integration tests | envtest binaries |
| E2E | `make test-e2e-smoke` | Manager deployment on Kind | Kind cluster + Docker |
| Bench | `make bench` | Performance benchmarks | None |

## Running Unit Tests

```bash
make test
```

This runs all packages except `internal/controller` (which requires envtest) and `test/e2e` (which requires a cluster). A `cover.out` file is generated automatically.

To run a specific package:

```bash
go test ./internal/cache/... -v
go test ./pkg/tools/... -v -run TestReadTarInfo
```

## Running Integration Tests (envtest)

Controller tests use the `envtest` framework, which provides a local API server and etcd without needing a full cluster.

```bash
# First time only: download Kubernetes binaries for envtest
make setup-envtest

# Run controller envtest tests
make test-envtest
```

## Building and Deploying for Manual Testing

```bash
# Build Docker image
make docker-build IMG=opensaola:v0.1.0

# Install CRDs into the current cluster
make install

# Deploy operator
make deploy IMG=opensaola:v0.1.0

# Verify
kubectl get pods -n opensaola-system
kubectl logs -n opensaola-system deploy/opensaola-controller-manager --tail=20

# Undeploy when done
make undeploy
make uninstall
```

## E2E Tests

### Automated (creates and destroys a Kind cluster)

```bash
make test-e2e-smoke
```

This target creates a temporary Kind cluster named `opensaola-e2e`, runs the e2e suite, and deletes the cluster afterward.

### Against an existing cluster

```bash
# Create a Kind cluster (if you don't have one)
make kind-create

# Run e2e tests
make test-e2e

# Clean up
make kind-delete
```

### Full E2E script

A convenience script that builds, deploys, and validates the operator end-to-end:

```bash
./scripts/e2e-full-test.sh

# Override the image tag:
IMG=opensaola:dev ./scripts/e2e-full-test.sh
```

## Code Coverage

```bash
# Run tests and open HTML coverage report in browser
make coverage
```

This runs `make test` first, then opens `cover.out` as an interactive HTML report.

## Benchmarks

```bash
make bench
```

To compare two benchmark runs:

```bash
make bench > /tmp/old.txt
# ... make changes ...
make bench > /tmp/new.txt
make benchstat BENCH_OLD=/tmp/old.txt BENCH_NEW=/tmp/new.txt
```

## Using saola-cli for E2E Validation

The [saola-cli](https://gitee.com/opensaola/saola-cli) project provides a CLI tool that can be used to validate operator behavior end-to-end.

### Prerequisites
- saola-cli binary built: `cd ../saola-cli && make build`
- A middleware package directory (e.g., `../dataservice-baseline/clickhouse`)

### Running saola-cli E2E
After deploying the operator (see "Build and Deploy for Testing" above), use the saola-cli E2E script:

```bash
cd ../saola-cli
PKG_DIR=../dataservice-baseline/clickhouse ./scripts/e2e-test.sh
```

This script covers: package install → baseline queries → operator deployment → middleware deployment → output format verification.

Sample YAML files for ClickHouse are available at `saola-cli/docs/e2e-samples/`.

## Lifecycle Test Scenarios

These manual test scenarios verify the operator's behavior across the full resource lifecycle. Run them after deploying the operator (see "Build and Deploy for Testing").

### Delete Lifecycle
1. Create a Middleware: `kubectl apply -f <middleware.yaml>`
2. Wait for Available: `kubectl get mid -n <ns> -w`
3. Delete: `kubectl delete mid <name> -n <ns>`
4. Verify finalizer runs: `kubectl get mid <name> -n <ns>` (should show Terminating briefly)
5. Verify pods terminate: `kubectl get pods -n <ns>` (middleware pods should disappear)
6. Verify child CRs removed: `kubectl get chi -n <ns>` (ClickHouseInstallation should be gone)

### Upgrade Lifecycle
1. Create Middleware with version 24.8
2. Annotate to trigger upgrade: `kubectl annotate mid <name> -n <ns> middleware.cn/update=true`
3. Verify state transitions: Available → Updating → Available
4. Verify pods updated with new configuration

### Error Recovery
1. Create Middleware with non-existent baseline: `spec.baseline: "nonexistent-baseline"`
2. Verify Unavailable state with error condition: `kubectl get mid <name> -n <ns> -o jsonpath='{.status}'`
3. Fix the baseline reference: `kubectl edit mid <name> -n <ns>`
4. Verify auto-recovery to Available

### Operator Restart Recovery
1. Create Middleware, wait for Available
2. Kill operator pod: `kubectl delete pod -n opensaola-system -l control-plane=controller-manager`
3. Wait for new pod: `kubectl get pods -n opensaola-system -w`
4. Verify Middleware state unchanged: `kubectl get mid -n <ns>`

## CI Checks (run locally before pushing)

```bash
make lint                                      # golangci-lint
make test                                      # unit tests
make manifests generate && git diff --exit-code # CRD drift check
```

## Package Coverage Matrix

Packages **with** tests:

| Package | Test Files |
|---------|-----------|
| `internal/controller` | 12 |
| `pkg/tools` | 6 |
| `internal/k8s` | 4 |
| `internal/service/packages` | 3 |
| `internal/service/middlewareoperator` | 3 |
| `test/e2e` | 2 |
| `api/v1` | 1 |
| `internal/cache` | 1 |
| `internal/concurrency` | 1 |
| `internal/k8s/kubeclient` | 1 |
| `internal/resource` | 1 |
| `internal/resource/logger` | 1 |
| `internal/service/consts` | 1 |
| `internal/service/customresource` | 1 |
| `internal/service/middleware` | 1 |
| `internal/service/middlewareaction` | 1 |
| `internal/service/middlewareactionbaseline` | 1 |
| `internal/service/middlewarebaseline` | 1 |
| `internal/service/middlewareconfiguration` | 1 |
| `internal/service/middlewareoperatorbaseline` | 1 |
| `internal/service/middlewarepackage` | 1 |
| `internal/service/status` | 1 |
| `internal/service/synchronizer` | 1 |
| `internal/service/watcher` | 1 |
| `pkg/config` | 1 |
| `pkg/metrics` | 1 |
| `pkg/tools/ctxkeys` | 1 |

Packages **without** tests:

| Package | Notes |
|---------|-------|
| `cmd` | Main entrypoint; tested via e2e |
| `test/utils` | Test utilities (not a testable package) |
