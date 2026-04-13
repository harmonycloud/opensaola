# pkg

Public packages for the OpenSaola operator.

## Directory Structure

| Package | Description |
|---------|-------------|
| `config` | Configuration management using Viper |
| `metrics` | Prometheus metrics definitions |
| `tools` | Shared utilities (template, YAML, CUE, archive) |

## Moved to internal/

The following packages have been moved to `internal/` as they are internal implementation details and should not be imported by external consumers:

| Old Path | New Path | Description |
|----------|----------|-------------|
| `pkg/concurrency` | `internal/concurrency` | Concurrency utilities and helpers |
| `pkg/k8s` | `internal/k8s` | Kubernetes client wrappers for CRD and native resources |
| `pkg/resource` | `internal/resource` | Resource management and logging utilities |
| `pkg/service` | `internal/service` | Core business logic for each CRD controller |
