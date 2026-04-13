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
