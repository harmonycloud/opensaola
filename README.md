# OpenSaola

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://img.shields.io/github/actions/workflow/status/OpenSaola/opensaola/ci.yml?branch=main&label=CI)](https://github.com/OpenSaola/opensaola/actions)

**English** | [中文](README_zh.md)

A Kubernetes operator for **middleware lifecycle management**. Declaratively install, upgrade, configure, monitor, and delete middleware instances (Redis, MySQL, Kafka, etc.) on Kubernetes clusters.

## Why OpenSaola?

Managing middleware on Kubernetes typically involves operator-per-middleware, each with its own API, upgrade path, and operational quirks. OpenSaola introduces a **unified, package-driven framework** that standardizes middleware operations across types:

- **One API for all middleware** -- manage Redis, MySQL, Kafka through the same CRD model
- **Package-driven** -- middleware capabilities are distributed as portable packages, not baked into the operator
- **Baseline templates** -- enforce organizational standards with reusable default specs
- **Pluggable actions** -- define lifecycle steps (install, upgrade, backup, restore) as CUE/Go/shell scripts
- **Independent configuration** -- apply config changes without full reconciliation cycles

## How It Works

OpenSaola is a **framework** -- it does not ship with any middleware out of the box. You teach it how to manage a middleware type by installing a **package**.

```
                    MiddlewarePackage
              (Secret with TAR archive)
   baselines / templates / actions / configs
                         |
                       install
          +--------------+--------------+---------------+
          |              |              |               |
          v              v              v               v
     Middleware     Operator       Action         Configuration
      Baseline      Baseline      Baseline         (templates)
          |              |
          v              v
      Middleware   MiddlewareOperator ---> Deployment
      (instance)    (manages a type)      (operator pod)
```

**The flow:**

1. **Package** a middleware definition (baselines + templates + actions) into a TAR archive, store it as a Kubernetes Secret
2. **Create** a `MiddlewarePackage` -- the operator unpacks baselines, configurations, and action definitions automatically
3. **Create** a `MiddlewareOperator` -- deploys the operator for that middleware type (e.g., Redis Operator)
4. **Create** a `Middleware` -- spins up an actual middleware instance using the baseline + user parameters

Each resource is driven through a state machine (`Checking → Creating → Running`) with progress reported via standard Kubernetes conditions.

## CRD Overview

| CRD | Short | Scope | Purpose |
|-----|-------|-------|---------|
| **Middleware** | `mid` | Namespaced | A middleware instance (Redis, MySQL, etc.) |
| **MiddlewareOperator** | `mo` | Namespaced | Manages operator deployment for a middleware type |
| **MiddlewareBaseline** | `mb` | Cluster | Default spec template for a middleware type |
| **MiddlewareOperatorBaseline** | `mob` | Cluster | Default spec template for an operator |
| **MiddlewareAction** | `ma` | Namespaced | Lifecycle action execution (install, upgrade, backup, etc.) |
| **MiddlewareActionBaseline** | `mab` | Cluster | Reusable action step definitions |
| **MiddlewareConfiguration** | `mcf` | Cluster | Runtime configuration templates |
| **MiddlewarePackage** | `mp` | Cluster | Packaged middleware distribution unit |

## Quick Start

### Prerequisites

- Kubernetes 1.21+
- Helm 3
- kubectl

### Install

```bash
helm install opensaola chart/opensaola \
  --namespace opensaola-system \
  --create-namespace
```

### Verify

```bash
# Check operator is running
kubectl get pods -n opensaola-system

# Check CRDs are installed
kubectl get crds | grep middleware.cn

# View all middleware instances
kubectl get mid -A
```

### Deploy Middleware

> **Note:** OpenSaola requires a middleware package to deploy actual middleware.
> Packages contain the templates and actions that define how each middleware type is managed.
> See the [documentation](docs/) for details on creating and installing packages.

```bash
# Apply a middleware package (stored as a Secret)
kubectl apply -f my-redis-package.yaml

# Create an operator for the middleware type
kubectl apply -f my-redis-operator.yaml

# Create a middleware instance
kubectl apply -f my-redis-instance.yaml

# Watch the state converge
kubectl get mid my-redis -w
```

## Configuration

OpenSaola is configured via Helm values. Key settings:

```yaml
# Operator config
config:
  dataNamespace: "middleware-operator"  # where package Secrets live
  logLevel: 0                          # 0=debug, 1=info, 2=warn, 3=error
  logFormat: "console"                 # "console" or "json"

# Resources
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi

# Monitoring
serviceMonitor:
  enabled: false    # enable for Prometheus auto-discovery
```

See [`chart/opensaola/values.yaml`](chart/opensaola/values.yaml) for all available options.

## Development

```bash
# Build
make build

# Run tests
make test                # unit tests
make test-envtest        # controller integration tests (envtest)
make test-e2e-smoke      # e2e tests (auto-creates Kind cluster)

# Code generation (after modifying api/v1/ types)
make generate-all        # generate code + manifests + sync CRDs to Helm chart

# Run locally
make kind-create         # create Kind cluster
make install             # install CRDs
make run                 # run operator locally

# Lint
make lint
```

Run `make help` to see all available targets.

## Documentation

| Document | Description |
|----------|-------------|
| [Technical Documentation](docs/opensaola-technical_en.md) | Architecture, CRD reference, reconcile flows, state machine |
| [Package Documentation](docs/opensaola-packaging_en.md) | Package format, baselines, actions, CUE templates, Redis case study |
| [Troubleshooting Guide](docs/troubleshooting.md) | Common issues, debugging commands, log configuration |
| [Upgrade Guide](docs/upgrade-guide.md) | Version upgrade procedures and rollback |
| [Testing Guide](docs/testing-guide.md) | Test tiers, running tests, coverage, benchmarks |
| [Contributing Guide](CONTRIBUTING.md) | Development setup, architecture, coding standards |

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, architecture overview, and guidelines.

## License

Copyright 2025 The OpenSaola Authors.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full license text.
