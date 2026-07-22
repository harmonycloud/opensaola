# Image URL to use all building/pushing image targets
IMG ?= controller:latest

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker
MANAGER_VERSION ?= $(shell \
	if tag="$$(git describe --exact-match --tags --match 'v[0-9]*' HEAD 2>/dev/null)" && \
		printf '%s\n' "$$tag" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$$'; then \
		printf '%s' "$$tag"; \
	elif branch="$$(git symbolic-ref --quiet --short HEAD 2>/dev/null)" && [ -n "$$branch" ]; then \
		printf '%s' "$$branch"; \
	elif commit="$$(git rev-parse --short=12 HEAD 2>/dev/null)" && [ -n "$$commit" ]; then \
		printf 'sha-%s' "$$commit"; \
	else \
		printf 'dev'; \
	fi)
MANAGER_GIT_COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf 'unknown')
MANAGER_BUILD_DATE ?= $(shell git show -s --format=%cI HEAD 2>/dev/null || printf 'unknown')
MANAGER_LDFLAGS ?= -s -w \
	-X github.com/harmonycloud/opensaola/internal/version.Version=$(MANAGER_VERSION) \
	-X github.com/harmonycloud/opensaola/internal/version.GitCommit=$(MANAGER_GIT_COMMIT) \
	-X github.com/harmonycloud/opensaola/internal/version.BuildDate=$(MANAGER_BUILD_DATE)
MANAGER_BUILD_ARGS ?= \
	--build-arg VERSION=$(MANAGER_VERSION) \
	--build-arg GIT_COMMIT=$(MANAGER_GIT_COMMIT) \
	--build-arg BUILD_DATE=$(MANAGER_BUILD_DATE)
SAOLA_CLI_LOCK ?= build/saola-cli-stable.lock
SAOLA_CLI_LOCK_HELPER ?= hack/saola-cli-lock.sh
override SAOLA_CLI_REPOSITORY = $(shell $(SAOLA_CLI_LOCK_HELPER) get $(SAOLA_CLI_LOCK) repository 2>/dev/null)
override SAOLA_CLI_VERSION = $(shell $(SAOLA_CLI_LOCK_HELPER) get $(SAOLA_CLI_LOCK) version 2>/dev/null)
override SAOLA_CLI_COMMIT = $(shell $(SAOLA_CLI_LOCK_HELPER) get $(SAOLA_CLI_LOCK) commit 2>/dev/null)
override SAOLA_CLI_SOURCE_DATE_EPOCH = $(shell $(SAOLA_CLI_LOCK_HELPER) get $(SAOLA_CLI_LOCK) source_date_epoch 2>/dev/null)
SAOLA_CLI_CONTEXT ?=
export SAOLA_CLI_CONTEXT
DOCKER_PLATFORM ?= linux/amd64
override SAOLA_CLI_BUILD_ARGS = \
	--build-arg SAOLA_CLI_VERSION=$(SAOLA_CLI_VERSION) \
	--build-arg SAOLA_CLI_COMMIT=$(SAOLA_CLI_COMMIT) \
	--build-arg SAOLA_CLI_SOURCE_DATE_EPOCH=$(SAOLA_CLI_SOURCE_DATE_EPOCH)

define with-saola-cli-context
	@source_repo="$${SAOLA_CLI_CONTEXT:-}"; \
	fetched_repo=''; \
	tmp_context=''; \
	cleanup() { \
		[[ -z "$$tmp_context" ]] || rm -rf "$$tmp_context"; \
		[[ -z "$$fetched_repo" ]] || rm -rf "$$fetched_repo"; \
	}; \
	trap cleanup EXIT; \
	if [[ -z "$$source_repo" ]]; then \
		if git -C ../saola-cli cat-file -e '$(SAOLA_CLI_COMMIT)^{commit}' 2>/dev/null; then \
			source_repo=../saola-cli; \
		else \
			fetched_repo="$$(mktemp -d "$${TMPDIR:-/tmp}/opensaola-saola-cli-repo.XXXXXX")"; \
			git -C "$$fetched_repo" init -q; \
			git -C "$$fetched_repo" remote add origin 'https://github.com/$(SAOLA_CLI_REPOSITORY).git'; \
			git -C "$$fetched_repo" fetch -q --depth=1 origin '$(SAOLA_CLI_COMMIT)' || { echo 'failed to fetch locked saola-cli commit' >&2; exit 1; }; \
			source_repo="$$fetched_repo"; \
		fi; \
	fi; \
	tmp_context="$$(mktemp -d "$${TMPDIR:-/tmp}/opensaola-saola-cli.XXXXXX")"; \
	git -C "$$source_repo" cat-file -e '$(SAOLA_CLI_COMMIT)^{commit}' || { echo 'locked saola-cli commit is unavailable in source repo' >&2; exit 1; }; \
	git -C "$$source_repo" archive '$(SAOLA_CLI_COMMIT)' | tar -x -C "$$tmp_context" || { echo 'failed to export locked saola-cli commit' >&2; exit 1; }; \
	context="$$tmp_context"; \
	$(1)
endef

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

KIND_CLUSTER ?= opensaola-e2e
HELM_RELEASE ?= opensaola
HELM_NAMESPACE_FROM_ALIAS := false
ifneq ($(origin n),undefined)
  ifeq ($(strip $(n)),)
    $(error n must not be empty; use n=<namespace>)
  endif
  HELM_NAMESPACE ?= $(n)
  HELM_NAMESPACE_FROM_ALIAS := true
else
HELM_NAMESPACE ?= middleware-operator
endif
HELM_AUTO_NAMESPACE ?= true
HELM_NAMESPACE_FROM_DEFAULT := $(if $(filter file,$(origin HELM_NAMESPACE)),$(if $(filter true,$(HELM_NAMESPACE_FROM_ALIAS)),false,true),false)
HELM_CHART ?= chart/opensaola
HELM_WAIT ?= false
HELM_TIMEOUT ?= 5m
HELM_GLOBAL_IMAGE_REGISTRY ?= ghcr.io
HELM_GLOBAL_IMAGE_REPOSITORY ?= harmonycloud
HELM_IMAGE_REGISTRY ?=
HELM_IMAGE_REPOSITORY ?=
HELM_KUBECTL_IMAGE_REGISTRY ?=
HELM_KUBECTL_IMAGE_REPOSITORY ?=
override HELM_MANAGER_IMAGE_NAME := opensaola
override HELM_KUBECTL_IMAGE_NAME := kubectl
trim-leading-slashes = $(if $(filter /%,$(1)),$(call trim-leading-slashes,$(patsubst /%,%,$(1))),$(1))
trim-trailing-slashes = $(if $(filter %/,$(1)),$(call trim-trailing-slashes,$(patsubst %/,%,$(1))),$(1))
normalize-image-prefix = $(call trim-trailing-slashes,$(call trim-leading-slashes,$(strip $(1))))
HELM_GLOBAL_IMAGE_REGISTRY_NORMALIZED := $(call normalize-image-prefix,$(HELM_GLOBAL_IMAGE_REGISTRY))
HELM_GLOBAL_IMAGE_REPOSITORY_NORMALIZED := $(call normalize-image-prefix,$(HELM_GLOBAL_IMAGE_REPOSITORY))
HELM_IMAGE_REGISTRY_NORMALIZED := $(call normalize-image-prefix,$(HELM_IMAGE_REGISTRY))
HELM_IMAGE_REPOSITORY_NORMALIZED := $(call normalize-image-prefix,$(HELM_IMAGE_REPOSITORY))
HELM_KUBECTL_IMAGE_REGISTRY_NORMALIZED := $(call normalize-image-prefix,$(HELM_KUBECTL_IMAGE_REGISTRY))
HELM_KUBECTL_IMAGE_REPOSITORY_NORMALIZED := $(call normalize-image-prefix,$(HELM_KUBECTL_IMAGE_REPOSITORY))
HELM_INTERNAL_REGISTRY_NORMALIZED := $(call normalize-image-prefix,$(HELM_INTERNAL_REGISTRY))
HELM_INTERNAL_REPOSITORY_NORMALIZED := $(call normalize-image-prefix,$(HELM_INTERNAL_REPOSITORY))
HELM_GIT_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null | tr '/' '-')
HELM_GIT_SHA ?= $(shell git rev-parse --short=7 HEAD 2>/dev/null)
HELM_GIT_TAG ?= $(shell git describe --exact-match --tags --match 'v[0-9]*' HEAD 2>/dev/null)
HELM_IMAGE_TAG ?= $(if $(HELM_GIT_TAG),$(HELM_GIT_TAG),$(if $(filter dev master main,$(HELM_GIT_BRANCH)),$(if $(HELM_GIT_SHA),sha-$(HELM_GIT_SHA),$(HELM_GIT_BRANCH)),dev))
HELM_IMAGE_PULL_POLICY ?= $(if $(filter dev master main latest,$(HELM_IMAGE_TAG)),Always,IfNotPresent)
HELM_INTERNAL_REGISTRY ?=
HELM_INTERNAL_REPOSITORY ?=
HELM_KUBECTL_IMAGE_TAG ?= v1.30.14
HELM_KUBECTL_IMAGE_PULL_POLICY ?= IfNotPresent
HELM_USE_INTERNAL_IMAGE := $(if $(and $(strip $(HELM_INTERNAL_REGISTRY)),$(strip $(HELM_INTERNAL_REPOSITORY))),true,false)
HELM_SOURCE_IMAGE_REGISTRY := $(if $(strip $(HELM_IMAGE_REGISTRY)),$(HELM_IMAGE_REGISTRY_NORMALIZED),$(HELM_GLOBAL_IMAGE_REGISTRY_NORMALIZED))
HELM_SOURCE_IMAGE_REPOSITORY := $(if $(strip $(HELM_IMAGE_REPOSITORY)),$(HELM_IMAGE_REPOSITORY_NORMALIZED),$(HELM_GLOBAL_IMAGE_REPOSITORY_NORMALIZED))
HELM_SOURCE_KUBECTL_IMAGE_REGISTRY := $(if $(strip $(HELM_KUBECTL_IMAGE_REGISTRY)),$(HELM_KUBECTL_IMAGE_REGISTRY_NORMALIZED),$(HELM_GLOBAL_IMAGE_REGISTRY_NORMALIZED))
HELM_SOURCE_KUBECTL_IMAGE_REPOSITORY := $(if $(strip $(HELM_KUBECTL_IMAGE_REPOSITORY)),$(HELM_KUBECTL_IMAGE_REPOSITORY_NORMALIZED),$(HELM_GLOBAL_IMAGE_REPOSITORY_NORMALIZED))
HELM_TARGET_IMAGE_REGISTRY := $(if $(filter true,$(HELM_USE_INTERNAL_IMAGE)),$(HELM_INTERNAL_REGISTRY_NORMALIZED),$(HELM_GLOBAL_IMAGE_REGISTRY_NORMALIZED))
HELM_TARGET_IMAGE_REPOSITORY := $(if $(filter true,$(HELM_USE_INTERNAL_IMAGE)),$(HELM_INTERNAL_REPOSITORY_NORMALIZED),$(HELM_GLOBAL_IMAGE_REPOSITORY_NORMALIZED))
HELM_DEPLOY_IMAGE_REGISTRY := $(if $(filter true,$(HELM_USE_INTERNAL_IMAGE)),$(HELM_TARGET_IMAGE_REGISTRY),$(HELM_SOURCE_IMAGE_REGISTRY))
HELM_DEPLOY_IMAGE_REPOSITORY := $(if $(filter true,$(HELM_USE_INTERNAL_IMAGE)),$(HELM_TARGET_IMAGE_REPOSITORY),$(HELM_SOURCE_IMAGE_REPOSITORY))
HELM_DEPLOY_KUBECTL_IMAGE_REGISTRY := $(if $(filter true,$(HELM_USE_INTERNAL_IMAGE)),$(HELM_TARGET_IMAGE_REGISTRY),$(HELM_SOURCE_KUBECTL_IMAGE_REGISTRY))
HELM_DEPLOY_KUBECTL_IMAGE_REPOSITORY := $(if $(filter true,$(HELM_USE_INTERNAL_IMAGE)),$(HELM_TARGET_IMAGE_REPOSITORY),$(HELM_SOURCE_KUBECTL_IMAGE_REPOSITORY))
HELM_SOURCE_IMAGE := $(HELM_SOURCE_IMAGE_REGISTRY)/$(HELM_SOURCE_IMAGE_REPOSITORY)/$(HELM_MANAGER_IMAGE_NAME):$(HELM_IMAGE_TAG)
HELM_TARGET_IMAGE := $(HELM_TARGET_IMAGE_REGISTRY)/$(HELM_TARGET_IMAGE_REPOSITORY)/$(HELM_MANAGER_IMAGE_NAME):$(HELM_IMAGE_TAG)
HELM_SOURCE_KUBECTL_IMAGE := $(HELM_SOURCE_KUBECTL_IMAGE_REGISTRY)/$(HELM_SOURCE_KUBECTL_IMAGE_REPOSITORY)/$(HELM_KUBECTL_IMAGE_NAME):$(HELM_KUBECTL_IMAGE_TAG)
HELM_TARGET_KUBECTL_IMAGE := $(HELM_TARGET_IMAGE_REGISTRY)/$(HELM_TARGET_IMAGE_REPOSITORY)/$(HELM_KUBECTL_IMAGE_NAME):$(HELM_KUBECTL_IMAGE_TAG)
HELM_SYNC_IMAGE ?= false
HELM_SYNC_MULTI_ARCH ?= true
HELM_REQUIRE_INTERNAL_IMAGE ?= false
HELM_EXTRA_ARGS ?=
HELM_REDEPLOY_AT ?= $(shell date -u +%Y%m%dT%H%M%SZ)

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: generate-all
generate-all: manifests generate sync-chart-crds ## Generate code, manifests, and sync CRDs to Helm chart.

.PHONY: sync-chart-crds
sync-chart-crds: ## Copy generated CRDs from config/crd/bases/ to chart/opensaola/files/crds/.
	@echo "Syncing CRDs to Helm chart..."
	@mkdir -p chart/opensaola/files/crds
	@cp config/crd/bases/*.yaml chart/opensaola/files/crds/
	@echo "Done. $$(ls config/crd/bases/*.yaml | wc -l | tr -d ' ') CRDs synced."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

# ---------------------------------------------------------------
# Testing Tiers:
#   make test          -- Unit tests (excludes controller envtest and e2e)
#   make test-race     -- Unit tests with Go race detector
#   make test-envtest  -- Controller integration tests (requires setup-envtest)
#   make test-e2e      -- E2E tests on existing Kind cluster
#   make test-e2e-smoke-- E2E tests (auto-creates/destroys Kind cluster)
#   make bench         -- Performance benchmarks
#   make coverage      -- Unit tests with HTML coverage report
# ---------------------------------------------------------------

.PHONY: test-makefile
test-makefile: ## Test Makefile targets that must not require the Go toolchain.
	bash hack/make-helm-deploy_test.sh

.PHONY: test-release-automation
test-release-automation:
	bash hack/cleanup-images-workflow_test.sh
	bash hack/manager-version-build_test.sh
	bash hack/manager-version-workflow_test.sh
	bash hack/manager-version_test.sh
	bash hack/resolve-saola-cli-release_test.sh
	bash hack/saola-cli-lock_test.sh

.PHONY: test
test: test-makefile test-release-automation manifests generate fmt vet ## Run tests.
	go test $$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | grep -v '^$$' | grep -v /e2e | grep -v /internal/controller) -coverprofile cover.out

.PHONY: coverage
coverage: test ## Open test coverage report in browser.
	go tool cover -html=cover.out

.PHONY: test-race
test-race: manifests generate fmt vet ## Run tests with race detector.
	go test -race $$(go list ./... | grep -v /e2e | grep -v /internal/controller) -count=1

.PHONY: test-envtest
test-envtest: manifests generate setup-envtest ## Run envtest integration tests only.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -tags envtest ./internal/controller/... -v -count=1

.PHONY: bench
bench: ## Run Go benchmarks (fast, no external deps).
	go test ./... -run '^$$' -bench . -benchmem

.PHONY: benchstat
benchstat: benchstat-bin ## Compare two benchmark outputs (BENCH_OLD, BENCH_NEW).
	@test -n "$(BENCH_OLD)" || { echo "BENCH_OLD is required"; exit 1; }
	@test -n "$(BENCH_NEW)" || { echo "BENCH_NEW is required"; exit 1; }
	$(BENCHSTAT) $(BENCH_OLD) $(BENCH_NEW)

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
.PHONY: test-e2e
test-e2e: manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	@command -v kind >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@kind get clusters | grep -qx '$(KIND_CLUSTER)' || { \
		echo "No Kind cluster named '$(KIND_CLUSTER)' is running. Create one via: kind create cluster --name $(KIND_CLUSTER)"; \
		exit 1; \
	}
	KIND_CLUSTER='$(KIND_CLUSTER)' CERT_MANAGER_INSTALL_SKIP='$(CERT_MANAGER_INSTALL_SKIP)' go test ./test/e2e/ -v -ginkgo.v

.PHONY: kind-create
kind-create: ## Create a Kind cluster for e2e (KIND_CLUSTER, default: opensaola-e2e).
	@command -v kind >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@kind get clusters | grep -qx '$(KIND_CLUSTER)' && { \
		echo "Kind cluster '$(KIND_CLUSTER)' already exists."; \
		exit 0; \
	} || true
	kind create cluster --name '$(KIND_CLUSTER)'

.PHONY: kind-delete
kind-delete: ## Delete the Kind cluster for e2e (KIND_CLUSTER, default: opensaola-e2e).
	@command -v kind >/dev/null 2>&1 || exit 0
	kind delete cluster --name '$(KIND_CLUSTER)' || true

.PHONY: test-e2e-smoke
test-e2e-smoke: manifests generate fmt vet ## Run e2e on a temporary Kind cluster (creates + deletes).
	@created=0; \
	if kind get clusters 2>/dev/null | grep -qx '$(KIND_CLUSTER)'; then \
		echo "Reusing Kind cluster '$(KIND_CLUSTER)'"; \
	else \
		created=1; \
		$(MAKE) kind-create KIND_CLUSTER='$(KIND_CLUSTER)'; \
	fi; \
	set +e; \
	KIND_CLUSTER='$(KIND_CLUSTER)' CERT_MANAGER_INSTALL_SKIP=true go test -tags=e2e ./test/e2e/ -v -ginkgo.v; \
	status=$$?; \
	set -e; \
	if [ $$created -eq 1 ]; then \
		$(MAKE) kind-delete KIND_CLUSTER='$(KIND_CLUSTER)' || true; \
	fi; \
	exit $$status

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -trimpath -ldflags "$(MANAGER_LDFLAGS)" -o bin/manager ./cmd

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
.PHONY: saola-cli-lock-validate
saola-cli-lock-validate:
	@$(SAOLA_CLI_LOCK_HELPER) validate $(SAOLA_CLI_LOCK)

.PHONY: docker-platform-validate
docker-platform-validate:
	@case '$(DOCKER_PLATFORM)' in linux/amd64|linux/arm64) ;; *) echo 'DOCKER_PLATFORM must be linux/amd64 or linux/arm64' >&2; exit 1 ;; esac

docker-build: saola-cli-lock-validate docker-platform-validate ## Build docker image with the manager.
	$(call with-saola-cli-context,$(CONTAINER_TOOL) build --build-context "saola-cli=$$context" $(MANAGER_BUILD_ARGS) $(SAOLA_CLI_BUILD_ARGS) --platform=$(DOCKER_PLATFORM) -t ${IMG} .)

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
override PLATFORMS = linux/amd64,linux/arm64
.PHONY: docker-buildx
docker-buildx: saola-cli-lock-validate ## Build and push docker image for the manager for cross-platform support
	$(call with-saola-cli-context,$(CONTAINER_TOOL) buildx build --build-context "saola-cli=$$context" $(MANAGER_BUILD_ARGS) $(SAOLA_CLI_BUILD_ARGS) --push --platform=$(PLATFORMS) --tag ${IMG} .)

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	@tmp_kustomize=$$(mktemp); \
	root_dir=$$(pwd); \
	cp "$$root_dir/config/manager/kustomization.yaml" "$$tmp_kustomize"; \
	trap 'cp "$$tmp_kustomize" "$$root_dir/config/manager/kustomization.yaml"; rm -f "$$tmp_kustomize"' EXIT; \
	cd "$$root_dir/config/manager" && $(KUSTOMIZE) edit set image controller=${IMG}; \
	cd "$$root_dir" && $(KUSTOMIZE) build config/default > "$$root_dir/dist/install.yaml"

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: helm-lint
helm-lint: ## Lint the OpenSaola Helm chart.
	$(HELM) lint $(HELM_CHART)

.PHONY: helm-template
helm-template: ## Render the OpenSaola Helm chart locally.
	$(HELM) template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) --include-crds >/tmp/opensaola-helm-template.yaml
	@echo "Rendered $(HELM_CHART) to /tmp/opensaola-helm-template.yaml"

.PHONY: helm-package
helm-package: ## Package the OpenSaola Helm chart into dist/charts/.
	@mkdir -p dist/charts
	$(HELM) package $(HELM_CHART) --destination dist/charts

.PHONY: verify-chart-crds
verify-chart-crds: ## Verify Helm chart CRDs match generated CRDs.
	@diff -qr config/crd/bases chart/opensaola/files/crds

.PHONY: test-helm-images
test-helm-images: ## Test Helm image prefix resolution.
	bash hack/helm-image-resolution_test.sh

.PHONY: helm-check
helm-check: helm-lint helm-template helm-package verify-chart-crds test-helm-images ## Run all Helm chart checks.

.PHONY: helm-deploy
helm-deploy: helm-upgrade ## Install or upgrade OpenSaola from the local Helm chart.

.PHONY: helm-deploy-dev
helm-deploy-dev: helm-upgrade-dev ## Upgrade OpenSaola with the floating dev image and force a rollout.

.PHONY: helm-sync-image
helm-sync-image: HELM_REQUIRE_INTERNAL_IMAGE=true
helm-sync-image: HELM_SYNC_IMAGE=true
helm-sync-image: sync-helm-image ## Sync Helm images to the configured internal registry without upgrading.

.PHONY: sync-helm-image
sync-helm-image:
	@if { [ -n "$(strip $(HELM_INTERNAL_REGISTRY))" ] && [ -z "$(strip $(HELM_INTERNAL_REPOSITORY))" ]; } || \
		{ [ -z "$(strip $(HELM_INTERNAL_REGISTRY))" ] && [ -n "$(strip $(HELM_INTERNAL_REPOSITORY))" ]; }; then \
		echo "HELM_INTERNAL_REGISTRY and HELM_INTERNAL_REPOSITORY must be set together." >&2; \
		exit 1; \
	fi; \
	if [ "$(HELM_USE_INTERNAL_IMAGE)" != "true" ]; then \
		if [ "$(HELM_REQUIRE_INTERNAL_IMAGE)" = "true" ] || [ "$(HELM_SYNC_IMAGE)" = "true" ]; then \
			echo "Set HELM_INTERNAL_REGISTRY and HELM_INTERNAL_REPOSITORY to sync Helm images." >&2; \
			exit 1; \
		fi; \
		exit 0; \
	fi; \
	if [ "$(HELM_SYNC_IMAGE)" != "true" ]; then \
		exit 0; \
	fi; \
	if [ -z "$(HELM_IMAGE_TAG)" ] || [ "$(HELM_IMAGE_TAG)" = "HEAD" ]; then \
		echo "Internal image sync requires HELM_IMAGE_TAG to resolve to a concrete tag." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_SOURCE_IMAGE_REGISTRY)" ]; then \
		echo "Manager source image registry is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_SOURCE_IMAGE_REPOSITORY)" ]; then \
		echo "Manager source image repository is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_SOURCE_KUBECTL_IMAGE_REGISTRY)" ]; then \
		echo "Kubectl source image registry is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_SOURCE_KUBECTL_IMAGE_REPOSITORY)" ]; then \
		echo "Kubectl source image repository is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_TARGET_IMAGE_REGISTRY)" ]; then \
		echo "Internal target image registry is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_TARGET_IMAGE_REPOSITORY)" ]; then \
		echo "Internal target image repository is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	sync_image() { \
		local source_image="$$1"; \
		local target_image="$$2"; \
		if [ "$$source_image" = "$$target_image" ]; then \
			echo "Image already uses target registry: $$target_image"; \
			return 0; \
		fi; \
		if command -v skopeo >/dev/null 2>&1 && skopeo inspect --raw --tls-verify=false "docker://$$target_image" >/dev/null 2>&1; then \
			echo "Image already exists in target registry: $$target_image"; \
			return 0; \
		fi; \
		echo "Syncing $$source_image -> $$target_image"; \
		if command -v skopeo >/dev/null 2>&1; then \
			if [ "$(HELM_SYNC_MULTI_ARCH)" = "true" ]; then \
				skopeo copy --all --dest-tls-verify=false "docker://$$source_image" "docker://$$target_image" || return $$?; \
			else \
				skopeo copy --dest-tls-verify=false "docker://$$source_image" "docker://$$target_image" || return $$?; \
			fi; \
		elif [ "$(HELM_SYNC_MULTI_ARCH)" = "true" ]; then \
			echo "skopeo is required for multi-architecture image sync. Install skopeo or set HELM_SYNC_MULTI_ARCH=false for single-architecture fallback." >&2; \
			exit 1; \
		elif command -v docker >/dev/null 2>&1; then \
			docker pull "$$source_image"; \
			docker tag "$$source_image" "$$target_image"; \
			docker push "$$target_image"; \
		elif command -v nerdctl >/dev/null 2>&1; then \
			nerdctl pull "$$source_image"; \
			nerdctl tag "$$source_image" "$$target_image"; \
			nerdctl push --insecure-registry "$$target_image"; \
		else \
			echo "No image sync tool found. Install skopeo, docker, or nerdctl." >&2; \
			exit 1; \
		fi; \
	}; \
	sync_image '$(HELM_SOURCE_IMAGE)' '$(HELM_TARGET_IMAGE)'; \
	sync_image '$(HELM_SOURCE_KUBECTL_IMAGE)' '$(HELM_TARGET_KUBECTL_IMAGE)'

.PHONY: helm-upgrade
helm-upgrade: sync-helm-image ## Install or upgrade OpenSaola from the local Helm chart.
	@if [ -z "$(HELM_DEPLOY_IMAGE_REGISTRY)" ]; then \
		echo "Manager deployment image registry is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_DEPLOY_IMAGE_REPOSITORY)" ]; then \
		echo "Manager deployment image repository is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_DEPLOY_KUBECTL_IMAGE_REGISTRY)" ]; then \
		echo "Kubectl deployment image registry is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	if [ -z "$(HELM_DEPLOY_KUBECTL_IMAGE_REPOSITORY)" ]; then \
		echo "Kubectl deployment image repository is empty after trimming whitespace and slashes." >&2; \
		exit 1; \
	fi; \
	release_namespace='$(HELM_NAMESPACE)'; \
	if [ "$(HELM_AUTO_NAMESPACE)" = "true" ] && [ "$(HELM_NAMESPACE_FROM_DEFAULT)" = "true" ]; then \
		detected_namespaces="$$( $(HELM) list -A --no-headers 2>/dev/null | awk -v release='$(HELM_RELEASE)' '$$1 == release { print $$2 }' )"; \
		detected_count="$$(printf '%s\n' "$$detected_namespaces" | sed '/^$$/d' | wc -l | tr -d '[:space:]')"; \
		if [ "$$detected_count" = "1" ]; then \
			release_namespace="$$(printf '%s\n' "$$detected_namespaces" | sed '/^$$/d' | head -n 1)"; \
			if [ "$$release_namespace" != "$(HELM_NAMESPACE)" ]; then \
				echo "Using existing Helm release namespace '$$release_namespace' for release '$(HELM_RELEASE)'."; \
			fi; \
		elif [ "$$detected_count" -gt 1 ]; then \
			echo "Multiple Helm releases named '$(HELM_RELEASE)' found in namespaces:" >&2; \
			printf '%s\n' "$$detected_namespaces" | sed '/^$$/d; s/^/  /' >&2; \
			echo "Set HELM_NAMESPACE=<namespace> to choose one." >&2; \
			exit 1; \
		fi; \
	fi; \
	tag_args=(); \
	if [ -n "$(HELM_IMAGE_TAG)" ] && [ "$(HELM_IMAGE_TAG)" != "HEAD" ]; then \
		tag_args+=(--set image.tag="$(HELM_IMAGE_TAG)"); \
	fi; \
	manager_image_args=(); \
	kubectl_image_args=(); \
	if [ "$(HELM_USE_INTERNAL_IMAGE)" != "true" ]; then \
		if [ -n "$(strip $(HELM_IMAGE_REGISTRY))" ]; then \
			manager_image_args+=(--set image.registry="$(HELM_IMAGE_REGISTRY_NORMALIZED)"); \
		fi; \
		if [ -n "$(strip $(HELM_IMAGE_REPOSITORY))" ]; then \
			manager_image_args+=(--set image.repository="$(HELM_IMAGE_REPOSITORY_NORMALIZED)"); \
		fi; \
		if [ -n "$(strip $(HELM_KUBECTL_IMAGE_REGISTRY))" ]; then \
			kubectl_image_args+=(--set kubectl.image.registry="$(HELM_KUBECTL_IMAGE_REGISTRY_NORMALIZED)"); \
		fi; \
		if [ -n "$(strip $(HELM_KUBECTL_IMAGE_REPOSITORY))" ]; then \
			kubectl_image_args+=(--set kubectl.image.repository="$(HELM_KUBECTL_IMAGE_REPOSITORY_NORMALIZED)"); \
		fi; \
	else \
		kubectl_image_args+=(--set kubectl.image.tag="$(HELM_KUBECTL_IMAGE_TAG)"); \
		kubectl_image_args+=(--set kubectl.image.pullPolicy="$(HELM_KUBECTL_IMAGE_PULL_POLICY)"); \
	fi; \
	wait_args=(); \
	if [ "$(HELM_WAIT)" = "true" ]; then \
		wait_args+=(--wait --timeout "$(HELM_TIMEOUT)"); \
	fi; \
	$(HELM) upgrade --install $(HELM_RELEASE) $(HELM_CHART) \
		--namespace "$$release_namespace" \
		--create-namespace \
		--set global.registry="$(HELM_TARGET_IMAGE_REGISTRY)" \
		--set global.repository="$(HELM_TARGET_IMAGE_REPOSITORY)" \
		--set image.pullPolicy="$(HELM_IMAGE_PULL_POLICY)" \
		"$${tag_args[@]}" \
		"$${manager_image_args[@]}" \
		"$${kubectl_image_args[@]}" \
		"$${wait_args[@]}" \
		$(HELM_EXTRA_ARGS)

.PHONY: helm-upgrade-dev
helm-upgrade-dev: ## Upgrade OpenSaola with the floating dev image and force a rollout.
	$(MAKE) helm-upgrade \
		HELM_IMAGE_TAG=dev \
		HELM_IMAGE_PULL_POLICY=Always \
		HELM_EXTRA_ARGS='--set-string podAnnotations.redeployAt=$(HELM_REDEPLOY_AT)'

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the OpenSaola Helm release.
	@release_namespace='$(HELM_NAMESPACE)'; \
	if [ "$(HELM_AUTO_NAMESPACE)" = "true" ] && [ "$(HELM_NAMESPACE_FROM_DEFAULT)" = "true" ]; then \
		detected_namespaces="$$( $(HELM) list -A --no-headers 2>/dev/null | awk -v release='$(HELM_RELEASE)' '$$1 == release { print $$2 }' )"; \
		detected_count="$$(printf '%s\n' "$$detected_namespaces" | sed '/^$$/d' | wc -l | tr -d '[:space:]')"; \
		if [ "$$detected_count" = "1" ]; then \
			release_namespace="$$(printf '%s\n' "$$detected_namespaces" | sed '/^$$/d' | head -n 1)"; \
			if [ "$$release_namespace" != "$(HELM_NAMESPACE)" ]; then \
				echo "Using existing Helm release namespace '$$release_namespace' for release '$(HELM_RELEASE)'."; \
			fi; \
		elif [ "$$detected_count" -gt 1 ]; then \
			echo "Multiple Helm releases named '$(HELM_RELEASE)' found in namespaces:" >&2; \
			printf '%s\n' "$$detected_namespaces" | sed '/^$$/d; s/^/  /' >&2; \
			echo "Set HELM_NAMESPACE=<namespace> to choose one." >&2; \
			exit 1; \
		fi; \
	fi; \
	$(HELM) uninstall $(HELM_RELEASE) --namespace "$$release_namespace" --ignore-not-found

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	@tmp_kustomize=$$(mktemp); \
	root_dir=$$(pwd); \
	cp "$$root_dir/config/manager/kustomization.yaml" "$$tmp_kustomize"; \
	trap 'cp "$$tmp_kustomize" "$$root_dir/config/manager/kustomization.yaml"; rm -f "$$tmp_kustomize"' EXIT; \
	cd "$$root_dir/config/manager" && $(KUSTOMIZE) edit set image controller=${IMG}; \
	cd "$$root_dir" && $(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: deploy-e2e
deploy-e2e: manifests kustomize ## Deploy controller (e2e overlay: feature gates enabled) to ~/.kube/config cluster.
	@tmp_kustomize=$$(mktemp); \
	root_dir=$$(pwd); \
	cp "$$root_dir/config/manager/kustomization.yaml" "$$tmp_kustomize"; \
	trap 'cp "$$tmp_kustomize" "$$root_dir/config/manager/kustomization.yaml"; rm -f "$$tmp_kustomize"' EXIT; \
	cd "$$root_dir/config/manager" && $(KUSTOMIZE) edit set image controller=${IMG}; \
	cd "$$root_dir" && $(KUSTOMIZE) build config/e2e | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: undeploy-e2e
undeploy-e2e: kustomize ## Undeploy controller (e2e overlay) from ~/.kube/config cluster.
	$(KUSTOMIZE) build config/e2e | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
HELM ?= helm
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
BENCHSTAT = $(LOCALBIN)/benchstat

## Tool Versions
KUSTOMIZE_VERSION ?= v5.5.0
CONTROLLER_TOOLS_VERSION ?= v0.17.2
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
PROJECT_GO_TOOLCHAIN ?= $(shell go env GOVERSION)
GOLANGCI_LINT_VERSION ?= v2.11.4

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@for i in 1 2 3; do \
		$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path && exit 0; \
		echo "Warning: setup-envtest failed (attempt $$i), retrying..."; \
		sleep 2; \
	done; \
	echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
	exit 1

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: benchstat-bin
benchstat-bin: $(BENCHSTAT) ## Download benchstat locally if necessary.
$(BENCHSTAT): $(LOCALBIN)
	$(call go-install-tool,$(BENCHSTAT),golang.org/x/perf/cmd/benchstat,latest)

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOTOOLCHAIN=$(PROJECT_GO_TOOLCHAIN)+auto GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef
