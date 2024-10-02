# Include images as env variables
include ./config/overlays/odh/params.env

# Include istio config as env variables
include ./config/samples/istio/components/istio.env

# Image URL to use all building/pushing image targets
IMG_REGISTRY ?= "quay.io"
IMG_ORG ?= "opendatahub"
IMG_REPO ?= "model-registry-operator"
IMG_VERSION ?= "latest"
IMG ?= "${IMG_REGISTRY}/${IMG_ORG}/${IMG_REPO}:${IMG_VERSION}"
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.28.0

# Kustomize overlay to use for deploy/undeploy
OVERLAY ?= "default"

# Disable operator webhooks by default for local testing
ENABLE_WEBHOOKS ?= false

# Enable Auth resources by default for local testing
CREATE_AUTH_RESOURCES ?= true

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

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

.PHONY: sync-images
sync-images:
	# sync model-registry image
	sed "s|quay.io/opendatahub/model-registry:.*|${IMAGES_REST_SERVICE}|" -i ./config/manager/manager.yaml
	sed "s|\"quay.io/opendatahub/model-registry:.*\"|\"${IMAGES_REST_SERVICE}\"|" -i ./internal/controller/config/defaults.go
	# sync mlmd image
	sed "s|quay.io/opendatahub/mlmd-grpc-server:.*|${IMAGES_GRPC_SERVICE}|" -i ./config/manager/manager.yaml
	sed "s|\"quay.io/opendatahub/mlmd-grpc-server:.*\"|\"${IMAGES_GRPC_SERVICE}\"|" -i ./internal/controller/config/defaults.go

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet govulncheck envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: sync-images manifests generate fmt vet govulncheck ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet govulncheck ## Run a controller from your host.
	ENABLE_WEBHOOKS=$(ENABLE_WEBHOOKS) CREATE_AUTH_RESOURCES=$(CREATE_AUTH_RESOURCES) go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-login
docker-login: ## Login to docker registry.
	$(CONTAINER_TOOL) login -u "${DOCKER_USER}" -p "${DOCKER_PWD}" "${IMG_REGISTRY}"

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name project-v3-builder
	$(CONTAINER_TOOL) buildx use project-v3-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm project-v3-builder
	rm Dockerfile.cross

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/$(OVERLAY) | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/$(OVERLAY) | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
ENVTEST_VERSION ?= v0.0.0-20240320141353-395cfc7486e6
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
GOVULNCHECK_VERSION ?= v1.1.3

## Tool Versions
KUSTOMIZE_VERSION ?= v5.1.1
CONTROLLER_TOOLS_VERSION ?= v0.13.0

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || GOBIN=$(LOCALBIN) GO111MODULE=on go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK) ## Download govulncheck locally if necessary. If wrong version is installed, it will be removed before downloading.
	$(GOVULNCHECK) ./...

$(GOVULNCHECK): $(LOCALBIN)
	@if test -x $(LOCALBIN)/govulncheck && ! $(LOCALBIN)/govulncheck -version | grep -q $(GOVULNCHECK_VERSION); then \
		echo "$(LOCALBIN)/govulncheck version is not expected $(GOVULNCHECK_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/govulncheck; \
	fi
	test -s $(LOCALBIN)/govulncheck || GOBIN=$(LOCALBIN) GO111MODULE=on go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: certificates
certificates:
	# generate TLS certs
	scripts/generate_certs.sh $(or $(DOMAIN),$(shell oc get ingresses.config/cluster -o jsonpath='{.spec.domain}'))
	# create secrets from TLS certs
	$(KUBECTL) create secret -n istio-system generic modelregistry-sample-rest-credential \
      --from-file=tls.key=certs/modelregistry-sample-rest.domain.key \
      --from-file=tls.crt=certs/modelregistry-sample-rest.domain.crt \
      --from-file=ca.crt=certs/domain.crt
	$(KUBECTL) create secret -n istio-system generic modelregistry-sample-grpc-credential \
      --from-file=tls.key=certs/modelregistry-sample-grpc.domain.key \
      --from-file=tls.crt=certs/modelregistry-sample-grpc.domain.crt \
      --from-file=ca.crt=certs/domain.crt
	$(KUBECTL) create secret generic model-registry-db-credential \
      --from-file=tls.key=certs/model-registry-db.key \
      --from-file=tls.crt=certs/model-registry-db.crt \
      --from-file=ca.crt=certs/domain.crt

.PHONY: certificates/clean
certificates/clean:
	# delete cert files
	mkdir -p certs
	rm -f certs/*
	# delete k8s secrets
	$(KUBECTL) delete --ignore-not-found=true -n istio-system secrets modelregistry-sample-rest-credential modelregistry-sample-grpc-credential
	$(KUBECTL) delete --ignore-not-found=true secrets model-registry-db-credential
