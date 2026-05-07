# OpenSaola

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://img.shields.io/github/actions/workflow/status/harmonycloud/opensaola/ci.yml?branch=master&label=CI)](https://github.com/harmonycloud/opensaola/actions)

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
helm upgrade --install opensaola ./chart/opensaola \
  --namespace opensaola-system \
  --create-namespace \
  --wait \
  --timeout 5m
```

From a source checkout, prefer the Makefile wrapper. It uses an exact `v*` tag when the current commit is a release tag; otherwise long-lived branches (`dev`, `master`, or `main`) deploy the current commit image tag (`sha-<shortsha>`). Short-lived feature branches fall back to `dev`. Override `HELM_IMAGE_TAG` when testing another release or SHA image:

```bash
make helm-deploy
```

If `HELM_NAMESPACE` is not set explicitly, the wrapper first looks for an existing `opensaola` release across all namespaces and upgrades it in place. If no release exists, it installs into `opensaola-system`. Set `n=<namespace>` (or `HELM_NAMESPACE=<namespace>`) to force a specific namespace.

For a server tracking `dev`, upgrade to the image built from the checked-out commit with:

```bash
git pull --ff-only && make helm-deploy
```

Run it after the GitHub Docker workflow for that commit has published the matching `sha-<shortsha>` image.

If the cluster pulls GHCR slowly, set only the internal Harbor registry and OpenSaola repository path. The Makefile syncs the current image to Harbor and deploys the internal image:

```bash
git pull --ff-only && \
HELM_INTERNAL_REGISTRY=10.10.102.124:443 \
HELM_INTERNAL_REPOSITORY=middleware/opensaola \
make helm-deploy
```

This keeps the default tag selection, so no manual tag is needed. The kubectl image used by the CRD hook Job is synced to the same Harbor project as well.

To follow the floating `dev` image tag and force a rollout, use:

```bash
make helm-deploy-dev
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
  dataNamespace: ""                     # empty defaults to the Helm release namespace
  createDataNamespace: false            # set true when using a separate data namespace
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

# Image
image:
  registry: "ghcr.io"
  repository: "harmonycloud/opensaola"
  tag: ""                              # empty defaults to Chart appVersion; releases override it from the Git tag
  pullPolicy: IfNotPresent             # use Always only for floating tags such as dev/master/latest
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
| [Release Process](docs/release-process.md) | Branch, image tag, and Helm chart release policy |
| [Contributing Guide](CONTRIBUTING.md) | Development setup, architecture, coding standards |

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, architecture overview, and guidelines.

## License

Copyright 2025 The OpenSaola Authors.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full license text.
