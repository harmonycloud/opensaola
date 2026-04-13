# pkg

Public packages for the OpenSaola operator.

## Directory Structure

| Package | Description |
|---------|-------------|
| `config` | Configuration management using Viper |
| `concurrency` | Concurrency utilities and helpers |
| `k8s` | Kubernetes client wrappers for CRD and native resources |
| `metrics` | Prometheus metrics definitions |
| `resource` | Resource management and logging utilities |
| `service` | Core business logic for each CRD controller |
| `tools` | Shared utilities (template, YAML, CUE, archive) |

## service sub-packages

| Package | Description |
|---------|-------------|
| `service/middleware` | Middleware instance lifecycle management |
| `service/middlewareoperator` | Operator deployment and RBAC management |
| `service/middlewarebaseline` | Baseline template management |
| `service/middlewareoperatorbaseline` | Operator baseline management |
| `service/middlewareaction` | Action execution (CUE, HTTP, CMD) |
| `service/middlewareactionbaseline` | Action baseline management |
| `service/middlewareconfiguration` | Runtime configuration deployment |
| `service/middlewarepackage` | Package extraction and validation |
| `service/packages` | Package cache and content management |
| `service/customresource` | Generic custom resource operations |
| `service/watcher` | Resource watch and event handling |
| `service/synchronizer` | State synchronization between CRs and workloads |
| `service/status` | Condition and status management |
| `service/consts` | Shared constants and error definitions |
