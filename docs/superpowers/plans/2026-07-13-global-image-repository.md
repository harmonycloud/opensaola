# Global Image Repository Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve manager and kubectl images from one global registry/repository prefix while keeping `opensaola` and `kubectl` fixed in Helm templates.

**Architecture:** Add `global.registry` and `global.repository` as shared defaults. Component registry/repository fields become optional prefix overrides; helpers append fixed names and fail closed when the resolved prefix is empty. The Makefile uses the same model for public deployment and internal-registry synchronization.

**Tech Stack:** Helm 3 templates and JSON schema, GNU Make, Bash regression tests, Kubernetes YAML.

## Global Constraints

- Defaults remain `ghcr.io/harmonycloud/opensaola` and `ghcr.io/harmonycloud/kubectl`.
- Final names `opensaola` and `kubectl` are not configurable.
- Component registry/repository fields are prefix overrides and do not contain final image names.
- Precedence is component override, then global value, then render failure.
- Tags, pull policies, image pull secrets, and multi-architecture synchronization remain unchanged.
- Do not edit or stage `docs/superpowers/plans/2026-07-13-ownerref-resource-sync.md`.

---

### Task 1: Helm image resolution

**Files:**
- Create: `hack/helm-image-resolution_test.sh`
- Modify: `chart/opensaola/values.yaml`
- Modify: `chart/opensaola/values.schema.json`
- Modify: `chart/opensaola/templates/_helpers.tpl`

**Interfaces:**
- Consumes: `global.registry`, `global.repository`, and optional component registry/repository prefixes.
- Produces: complete image strings from `opensaola.image` and `opensaola.kubectlImage`.

- [ ] **Step 1: Write the failing Helm test**

Create `hack/helm-image-resolution_test.sh` with these cases:

```bash
#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="${repo_root}/chart/opensaola"

assert_image() {
  local rendered="$1" expected="$2"
  grep -Fq "image: \"${expected}\"" <<<"${rendered}" || {
    echo "FAIL: missing image ${expected}" >&2
    exit 1
  }
}

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator)"
assert_image "${rendered}" 'ghcr.io/harmonycloud/opensaola:dev'
assert_image "${rendered}" 'ghcr.io/harmonycloud/kubectl:v1.30.14'

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator \
  --set-string global.registry=registry.example.com \
  --set-string global.repository=platform)"
assert_image "${rendered}" 'registry.example.com/platform/opensaola:dev'
assert_image "${rendered}" 'registry.example.com/platform/kubectl:v1.30.14'

rendered="$(helm template opensaola "${chart}" --namespace middleware-operator \
  --set-string image.registry=manager.example.com \
  --set-string image.repository=operators \
  --set-string kubectl.image.registry=tools.example.com \
  --set-string kubectl.image.repository=platform-tools)"
assert_image "${rendered}" 'manager.example.com/operators/opensaola:dev'
assert_image "${rendered}" 'tools.example.com/platform-tools/kubectl:v1.30.14'

error_file="$(mktemp)"
trap 'rm -f "${error_file}"' EXIT
if helm template opensaola "${chart}" --set-string global.repository= \
  --set-string image.repository= >/dev/null 2>"${error_file}"; then
  echo 'FAIL: empty manager repository prefix was accepted' >&2
  exit 1
fi
grep -Fq 'manager image repository prefix is required' "${error_file}"
echo 'PASS: Helm image prefixes resolve with fixed image names'
```

- [ ] **Step 2: Verify RED**

Run `chmod +x hack/helm-image-resolution_test.sh && bash hack/helm-image-resolution_test.sh`.

Expected: FAIL because global overrides are ignored by the current helpers.

- [ ] **Step 3: Add shared defaults and empty component overrides**

Add to `chart/opensaola/values.yaml`:

```yaml
global:
  registry: "ghcr.io"
  repository: harmonycloud
```

Set `image.registry`, `image.repository`, `kubectl.image.registry`, and `kubectl.image.repository` to `""`, with bilingual comments that they override prefixes only.

- [ ] **Step 4: Update the values schema**

Add:

```json
"global": {
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "registry": { "type": "string" },
    "repository": { "type": "string" }
  }
}
```

Remove `minLength: 1` from both component repository fields. Retain `additionalProperties: false` so an `image.name` value is rejected.

- [ ] **Step 5: Implement the helpers**

Use these exact helper bodies in `_helpers.tpl`:

```gotemplate
{{- define "opensaola.image" -}}
{{- $registry := default .Values.global.registry .Values.image.registry | trimAll "/" -}}
{{- $repository := default .Values.global.repository .Values.image.repository | trimAll "/" -}}
{{- $registry = required "manager image registry is required" $registry -}}
{{- $repository = required "manager image repository prefix is required" $repository -}}
{{- printf "%s/%s/opensaola:%s" $registry $repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end }}

{{- define "opensaola.kubectlImage" -}}
{{- $registry := default .Values.global.registry .Values.kubectl.image.registry | trimAll "/" -}}
{{- $repository := default .Values.global.repository .Values.kubectl.image.repository | trimAll "/" -}}
{{- $registry = required "kubectl image registry is required" $registry -}}
{{- $repository = required "kubectl image repository prefix is required" $repository -}}
{{- printf "%s/%s/kubectl:%s" $registry $repository .Values.kubectl.image.tag -}}
{{- end }}
```

- [ ] **Step 6: Verify GREEN and commit**

```bash
bash hack/helm-image-resolution_test.sh
helm lint chart/opensaola
git add chart/opensaola/values.yaml chart/opensaola/values.schema.json \
  chart/opensaola/templates/_helpers.tpl hack/helm-image-resolution_test.sh
git commit -m "feat(chart): add global image repository prefix"
```

Expected: test PASS and Helm reports `1 chart(s) linted, 0 chart(s) failed`.

### Task 2: Makefile deployment and synchronization

**Files:**
- Modify: `Makefile`
- Modify: `hack/make-helm-deploy_test.sh`

**Interfaces:**
- Consumes: global public prefixes, optional component prefix overrides, and the internal prefix.
- Produces: source/target paths ending in fixed `/opensaola` and `/kubectl` names.

- [ ] **Step 1: Extend the Makefile test before implementation**

Assert the default dry-run output contains:

```bash
grep -Fq -- '--set global.registry="ghcr.io"' <<<"${output}"
grep -Fq -- '--set global.repository="harmonycloud"' <<<"${output}"
grep -Fq 'ghcr.io/harmonycloud/opensaola:sha-' <<<"${output}"
grep -Fq 'ghcr.io/harmonycloud/kubectl:v1.30.14' <<<"${output}"
```

Add an internal dry run:

```bash
internal_output="$(PATH="${minimal_path}" "${make_bin}" -C "${repo_root}" -n helm-deploy \
  HELM_INTERNAL_REGISTRY=registry.internal \
  HELM_INTERNAL_REPOSITORY=middleware \
  HELM_SYNC_IMAGE=true 2>&1)"
grep -Fq 'registry.internal/middleware/opensaola' <<<"${internal_output}"
grep -Fq 'registry.internal/middleware/kubectl' <<<"${internal_output}"
```

- [ ] **Step 2: Verify RED**

Run `bash hack/make-helm-deploy_test.sh`.

Expected: FAIL because the current Makefile passes complete component repositories.

- [ ] **Step 3: Define and resolve prefixes**

Replace existing image defaults with:

```make
HELM_GLOBAL_IMAGE_REGISTRY ?= ghcr.io
HELM_GLOBAL_IMAGE_REPOSITORY ?= harmonycloud
HELM_IMAGE_REGISTRY ?=
HELM_IMAGE_REPOSITORY ?=
HELM_KUBECTL_IMAGE_REGISTRY ?=
HELM_KUBECTL_IMAGE_REPOSITORY ?=
HELM_MANAGER_IMAGE_NAME := opensaola
HELM_KUBECTL_IMAGE_NAME := kubectl
```

Resolve each public prefix using component-over-global precedence, then build:

```make
HELM_SOURCE_IMAGE := $(HELM_SOURCE_IMAGE_REGISTRY)/$(HELM_SOURCE_IMAGE_REPOSITORY)/$(HELM_MANAGER_IMAGE_NAME):$(HELM_IMAGE_TAG)
HELM_SOURCE_KUBECTL_IMAGE := $(HELM_SOURCE_KUBECTL_IMAGE_REGISTRY)/$(HELM_SOURCE_KUBECTL_IMAGE_REPOSITORY)/$(HELM_KUBECTL_IMAGE_NAME):$(HELM_KUBECTL_IMAGE_TAG)
```

Treat `HELM_INTERNAL_REPOSITORY` as one shared prefix and append both fixed names to target references.

- [ ] **Step 4: Pass global values to Helm**

Replace default manager-specific flags with:

```bash
--set global.registry="$(HELM_TARGET_IMAGE_REGISTRY)" \
--set global.repository="$(HELM_TARGET_IMAGE_REPOSITORY)" \
```

Pass component flags only when their overrides are non-empty and internal synchronization is not replacing them with the internal global prefix.

- [ ] **Step 5: Integrate chart tests and verify GREEN**

Add `test-helm-images` as a phony target running `bash hack/helm-image-resolution_test.sh`, and add it to the `helm-check` prerequisites.

Run:

```bash
bash hack/make-helm-deploy_test.sh
make -n helm-deploy n=custom-namespace | grep -F "release_namespace='custom-namespace';"
make -n helm-deploy HELM_NAMESPACE=other-namespace | grep -F "release_namespace='other-namespace';"
```

- [ ] **Step 6: Commit the Makefile unit**

This commit also includes the already approved no-Go parsing and `middleware-operator` namespace changes in these same files.

```bash
git add Makefile hack/make-helm-deploy_test.sh
git commit -m "fix: simplify Helm deployment defaults"
```

### Task 3: Documentation

**Files:**
- Modify: `README.md`
- Modify: `README_zh.md`
- Modify: `chart/opensaola/README.md`
- Modify: `chart/opensaola/README_zh.md`

**Interfaces:**
- Consumes: values and Makefile names from Tasks 1-2.
- Produces: public GHCR and internal Harbor examples.

- [ ] **Step 1: Document global and component prefixes**

Document:

```yaml
global:
  registry: ghcr.io
  repository: harmonycloud
```

Explain that fixed names are appended. Show a component override using prefix-only values such as `operators` and `platform-tools`.

- [ ] **Step 2: Update internal examples and migration guidance**

Change all examples to `HELM_INTERNAL_REPOSITORY=middleware`, which produces `middleware/opensaola` and `middleware/kubectl`. State that old custom values ending in `/opensaola` or `/kubectl` must remove that suffix.

- [ ] **Step 3: Commit documentation**

This commit also includes the already approved `middleware-operator` namespace documentation edits.

```bash
git add README.md README_zh.md chart/opensaola/README.md chart/opensaola/README_zh.md
git commit -m "docs: explain Helm image and namespace defaults"
```

### Task 4: Full verification

**Files:** Verify only.

**Interfaces:**
- Consumes: all earlier tasks.
- Produces: local release evidence without touching unrelated work.

- [ ] **Step 1: Run focused checks**

```bash
bash -n hack/make-helm-deploy_test.sh
bash -n hack/helm-image-resolution_test.sh
bash hack/make-helm-deploy_test.sh
bash hack/helm-image-resolution_test.sh
```

- [ ] **Step 2: Run project checks**

```bash
make test
make helm-check
helm template opensaola chart/opensaola --namespace middleware-operator \
  | grep -E 'image: "ghcr.io/harmonycloud/(opensaola|kubectl):'
```

Expected: all checks pass and both fixed image names render without duplicate path segments.

- [ ] **Step 3: Verify scope**

```bash
git diff --check
git status --short --branch
```

Expected: no whitespace errors; the ownerRef plan remains untracked and unchanged. Do not push without a separate user request.
