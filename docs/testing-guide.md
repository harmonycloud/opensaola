# OpenSaola Testing Guide

> Chinese version: [testing-guide_zh.md](testing-guide_zh.md)

This guide covers how to run every level of tests in the OpenSaola project, from fast unit tests to full end-to-end validation on a Kind cluster.

## Choose a validation profile

| Profile | Command | Coverage |
|---|---|---|
| PR | `bash scripts/test_all_gates.sh pr` | Units, race, lint, envtest, build, Helm, controller smoke, Redis fields and catalog retention |
| Release | `make test-release` | PR checks with race-enabled envtest, an in-place Helm upgrade, six original-package cases and active fuzzing |
| Performance | `make test-performance` | Go benchmarks and alternating baseline/candidate field writes against fresh API servers |
| Full | `make test-full` | Release and performance, sequentially |

Run from the repository root with Go, Python 3, Docker, Kind, kubectl and Helm. Point `MIDDLEWARE_FIXTURES` to the [original-package fixtures](middleware-matrix.md) for release runs. Every attempt writes a fresh directory under `TEST_REPORT_DIR` (default: system temp) containing a per-gate log and `summary.tsv`. Direct script exit codes are 0 for all passed, 1 if any gate failed and 77 if blocked with no other failures. Make reports its own nonzero exit code; consult the summary for individual results.

Helm upgrades use the latest stable local tag and the default CRD hook. Set `HELM_PREVIOUS_VERSION=1.5.1` to cross the Configuration checkpoint schema change, `HELM_PREVIOUS_CHART` for an offline chart and `HELM_CANDIDATE_IMAGE` for a manager built from working-tree changes. The test checks the data, PVC, CRDs, controller restart and cleanup of a partially published Configuration batch.

Performance inputs are `PERF_BASE_REF`, `PERF_PAIRS` and `KUBEBUILDER_ASSETS`. The same probe runs on both revisions, alternating order. Compare throughput and P95 for 1 KiB at 8 workers and 64 KiB at 32 workers on an idle host. Incorrect data, failed withdrawal and any write on unchanged input fail the test.

`make test-envtest` also requires PASS events for named historical behavior checks, including both accepted API defaults on a 5Gi claim and rejected rewrites to 1Gi. Update the [behavior mapping](../hack/check-regression-events.py) alongside intentional changes to those tests.

For topology-specific checks (MinIO node loss and dual storage classes, ZooKeeper election and restart, and Kafka ACLs), use the [periodic runtime checklist](periodic-runtime-checks_zh.md) when those dependencies change and record their outcomes separately.

## Test Tiers Overview

| Tier | Command | Scope | Dependencies |
|------|---------|-------|--------------|
| Unit | `make test` | Default tests, including controller units, excluding cluster E2E | None |
| Race | `make test-race` | Same as unit, with race detector | None |
| Envtest | `make test-envtest` | Controller integration tests | envtest binaries |
| E2E | `make test-e2e-smoke` | Manager deployment on Kind | Kind cluster + Docker |
| Bench | `make bench` | Performance benchmarks | None |

## Running Unit Tests

```bash
make test
```

This runs packages with default-tagged tests and writes `cover.out`. Run `make test-envtest` for API server integration cases.

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

## Isolated Kind clusters

`make test-e2e-smoke`, `make test-e2e`, and `scripts/e2e-full-test.sh` run the controller smoke suite in a new local Kind cluster with a private kubeconfig. The runner cleans up its cluster on exit.

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
2. Annotate to trigger upgrade: `kubectl annotate mid <name> -n <ns> middleware.cn/update=<target-version> middleware.cn/baseline=<target-baseline>`
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
