
# Image URL to use all building/pushing image targets
IMG ?= quay.io/opendatahub/odh-workbenches-operator:odh-stable
# Container engine to use for building and pushing images (podman or docker)
CONTAINER_ENGINE ?= podman
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.32.0

# Get the currently used golang install path (in GOBIN, currentl directory or GOPATH/bin)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Use existing local.mk for dev overrides
-include local.mk

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests-fetch
manifests-fetch: ## Fetch upstream component manifests into opt/manifests/ for local development.
	bash get_all_manifests.sh

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

##@ Testing

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test $$(go list ./... | grep -v /e2e | grep -v /tests/) -coverprofile cover.out

.PHONY: unit-test
unit-test: manifests generate envtest ## Run unit tests (no fmt/vet check).
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test $$(go list ./... | grep -v /e2e | grep -v /tests/) -coverprofile cover.out

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

##@ Helm chart

CHART_DIR ?= charts/operator
HELM_IMAGE ?= docker.io/alpine/helm@sha256:8af788e831994fb426290aa30d8a0dbfefd4f32a3bbe851c4129d28db360c9d8

.PHONY: chart-sync-crd
chart-sync-crd: manifests ## Copy generated Workbenches CRD into the Helm chart (generated; not a second source of truth).
	mkdir -p "$(CHART_DIR)/crd"
	cp config/crd/bases/components.platform.opendatahub.io_workbenches.yaml "$(CHART_DIR)/crd/workbenches.crd.yaml"

.PHONY: chart-verify-params
chart-verify-params: ## Verify params.env matches values.yaml default manager image.
	@test "$$(grep '^workbenches-operator-image=' "$(CHART_DIR)/params.env" | cut -d= -f2-)" = \
		"$$(grep 'workbenchesOperatorImage:' "$(CHART_DIR)/values.yaml" | awk '{print $$2}')" || \
		(echo "params.env workbenches-operator-image must match values.params.workbenchesOperatorImage" && exit 1)

.PHONY: helm-lint
helm-lint: chart-sync-crd chart-verify-params ## Lint the operator Helm chart.
	@if command -v helm >/dev/null 2>&1; then \
		helm lint "$(CHART_DIR)"; \
	else \
		podman run --rm -v "$(CURDIR)/$(CHART_DIR):/chart:Z" "$(HELM_IMAGE)" lint /chart; \
	fi

.PHONY: helm-template
helm-template: chart-sync-crd chart-verify-params ## Render the operator Helm chart locally.
	@if command -v helm >/dev/null 2>&1; then \
		helm template workbenches-operator "$(CHART_DIR)" \
			--namespace workbenches-operator-system \
			--set applicationsNamespace=opendatahub; \
	else \
		podman run --rm -v "$(CURDIR)/$(CHART_DIR):/chart:Z" "$(HELM_IMAGE)" \
			template workbenches-operator /chart \
			--namespace workbenches-operator-system \
			--set applicationsNamespace=opendatahub; \
	fi

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: image-build
image-build: ## Build the operator container image.
	$(CONTAINER_ENGINE) build -t $(IMG) .

.PHONY: image-push
image-push: ## Push the operator container image to a registry.
	$(CONTAINER_ENGINE) push $(IMG)

.PHONY: image-build-push
image-build-push: image-build image-push ## Build and push the operator container image.

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller="$(IMG)"
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0
ENVTEST_VERSION ?= release-0.23

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

# go-install-tool will 'go install' any package with custom target and target directory.
# $1 - target path, $2 - package, $3 - version
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
	set -e; \
	package="$(2)@$(3)" ;\
	echo "Downloading $${package}" ;\
	rm -f "$(1)" || true ;\
	GOBIN="$(LOCALBIN)" go install $${package} ;\
	mv "$(1)" "$(1)-$(3)" ;\
}
@ln -sf "$(1)-$(3)" "$(1)"
endef
