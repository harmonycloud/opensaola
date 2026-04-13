# OpenSaola

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)

## Overview

OpenSaola is a Kubernetes operator for middleware lifecycle management. It provides a declarative, CRD-driven approach to install, upgrade, configure, monitor, and delete middleware instances (such as Redis, MySQL, Kafka, and more) on Kubernetes clusters. By leveraging baselines and action templates, OpenSaola standardizes middleware operations and reduces operational complexity at scale.

## Features

- **Declarative middleware management** -- define middleware instances as Kubernetes custom resources
- **Full lifecycle support** -- install, upgrade, configure, monitor, and delete middleware through CRD reconciliation
- **Baseline templates** -- reusable default specifications for middleware and operators, enabling consistent deployments
- **Pluggable action system** -- define and compose lifecycle actions (install, upgrade, delete, custom) as first-class resources
- **Configuration management** -- apply and track runtime configuration changes independently from the middleware spec
- **Package distribution** -- package middleware definitions for portable distribution across clusters
- **Multi-middleware support** -- manage any middleware type (Redis, MySQL, Kafka, Elasticsearch, etc.) through a unified API
- **Condition-based status tracking** -- detailed phase and condition reporting for observability

## Architecture

OpenSaola organizes middleware management around a layered CRD model. At the core, **Middleware** and **MiddlewareOperator** represent running instances and their managing operators, respectively. Each of these can reference a **Baseline** resource that provides default spec templates, reducing boilerplate and enforcing organizational standards.

**MiddlewareAction** resources define the concrete steps for lifecycle operations (install, upgrade, delete), and can also be templated through **MiddlewareActionBaseline** resources. **MiddlewareConfiguration** handles runtime configuration separately from the main spec, allowing config changes without full reconciliation cycles. Finally, **MiddlewarePackage** bundles all related resources into a distributable unit.

The operator watches these CRDs and drives each resource through a state machine (Checking, Creating, Updating, Running, Failed, etc.), reporting progress via standard Kubernetes conditions.

## CRD Overview

| CRD | Short Name | Purpose |
|-----|------------|---------|
| Middleware | `mid` | Represents a middleware instance (e.g., Redis, MySQL, Kafka) |
| MiddlewareOperator | `mo` | The operator that manages a specific middleware type |
| MiddlewareBaseline | -- | Default spec template for a middleware type |
| MiddlewareOperatorBaseline | -- | Default spec template for an operator |
| MiddlewareAction | -- | Defines lifecycle actions (install, upgrade, delete, etc.) |
| MiddlewareActionBaseline | -- | Default action templates |
| MiddlewareConfiguration | -- | Runtime configuration for middleware instances |
| MiddlewarePackage | -- | Packaged middleware distribution unit |

## Prerequisites

- Go 1.24+
- Kubernetes 1.21+
- kubectl
- Helm 3

## Quick Start

Install OpenSaola using Helm:

```bash
helm install opensaola chart/opensaola \
  --namespace opensaola-system \
  --create-namespace
```

Once installed, you can create middleware instances by applying Middleware custom resources:

```bash
kubectl apply -f config/samples/
```

## Development

```bash
# Build the operator binary
make build

# Run tests
make test

# Generate CRD manifests (WebhookConfiguration, ClusterRole, CRDs)
make manifests

# Generate deepcopy and other code
make generate

# Build and push the container image
make docker-build docker-push IMG=<registry>/opensaola:<tag>

# Install CRDs into the cluster
make install

# Deploy the operator to the cluster
make deploy IMG=<registry>/opensaola:<tag>
```

Run `make help` to see all available targets.

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to get involved.

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full license text.
