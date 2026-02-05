CONTAINER_IMG_REG ?= my-registry/my-namespace
LAUNCHER_IMG_REPO ?= launcher
LAUNCHER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
LAUNCHER_IMAGE := $(CONTAINER_IMG_REG)/$(LAUNCHER_IMG_REPO):$(LAUNCHER_IMG_TAG)

REQUESTER_IMG_REPO ?= requester
REQUESTER_IMG_TAG ?= latest
REQUESTER_IMAGE := $(CONTAINER_IMG_REG)/$(REQUESTER_IMG_REPO):$(REQUESTER_IMG_TAG)

CONTROLLER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
CONTROLLER_IMG ?= $(CONTAINER_IMG_REG)/dual-pods-controller:$(CONTROLLER_IMG_TAG)
TEST_REQUESTER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
TEST_REQUESTER_IMG ?= $(CONTAINER_IMG_REG)/test-requester:$(TEST_REQUESTER_IMG_TAG)
TEST_SERVER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
TEST_SERVER_IMG ?= $(CONTAINER_IMG_REG)/test-server:$(TEST_SERVER_IMG_TAG)
TEST_LAUNCHER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
TEST_LAUNCHER_IMG ?= $(CONTAINER_IMG_REG)/test-launcher:$(TEST_LAUNCHER_IMG_TAG)
POPULATOR_IMG_TAG ?= $(shell git rev-parse --short HEAD)
POPULATOR_IMG ?= $(CONTAINER_IMG_REG)/launcher-populator:$(POPULATOR_IMG_TAG)

TARGETARCH ?= $(shell go env GOARCH)

CLUSTER_NAME ?= fmatest

.PHONY: build-launcher
build-launcher:
	docker build -t $(LAUNCHER_IMAGE) -f dockerfiles/Dockerfile.launcher.benchmark . --progress=plain --platform linux/amd64

.PHONY: push-launcher
push-launcher:
	docker push $(LAUNCHER_IMAGE)

.PHONY: build-and-push-launcher
build-and-push-launcher:
	docker buildx build --push -t $(LAUNCHER_IMAGE) -f dockerfiles/Dockerfile.launcher.benchmark . --progress=plain --platform linux/amd64

.PHONY: build-requester
build-requester:
	docker build -t $(REQUESTER_IMAGE) -f dockerfiles/Dockerfile.requester . --progress=plain --platform=linux/$(TARGETARCH)

.PHONY: push-requester
push-requester:
	docker push $(REQUESTER_IMAGE)

.PHONY: build--and-push-requester ## multi-platform
build-and-push-requester:
	docker buildx build --push -t $(REQUESTER_IMAGE) -f dockerfiles/Dockerfile.requester . --progress=plain --platform linux/amd64,linux/arm64

.PHONY: build-controller-local
build-controller-local:
	KO_DOCKER_REPO=ko.local ko build -B ./cmd/dual-pods-controller -t ${CONTROLLER_IMG_TAG} --platform linux/$(shell go env GOARCH)
	docker tag ko.local/dual-pods-controller:${CONTROLLER_IMG_TAG} ${CONTROLLER_IMG}

.PHONY: load-controller-local
load-controller-local:
	kind load docker-image ${CONTROLLER_IMG} --name ${CLUSTER_NAME}

.PHONY: build-controller
build-controller:
	KO_DOCKER_REPO=$(CONTAINER_IMG_REG) ko build -B ./cmd/dual-pods-controller -t ${CONTROLLER_IMG_TAG} --platform all

.PHONY: build-test-requester-local
build-test-requester-local:
	KO_DOCKER_REPO=ko.local ko build -B ./cmd/test-requester -t ${TEST_REQUESTER_IMG_TAG} --platform linux/$(shell go env GOARCH)
	docker tag ko.local/test-requester:${TEST_REQUESTER_IMG_TAG} ${TEST_REQUESTER_IMG}

.PHONY: load-test-requester-local
load-test-requester-local:
	kind load docker-image ${TEST_REQUESTER_IMG} --name ${CLUSTER_NAME}

.PHONY: build-test-server-local
build-test-server-local:
	KO_DOCKER_REPO=ko.local ko build -B ./cmd/test-server -t ${TEST_SERVER_IMG_TAG} --platform linux/$(shell go env GOARCH)
	docker tag ko.local/test-server:${TEST_SERVER_IMG_TAG} ${TEST_SERVER_IMG}

.PHONY: load-test-server-local
load-test-server-local:
	kind load docker-image ${TEST_SERVER_IMG} --name ${CLUSTER_NAME}

.PHONY: build-test-launcher-local
build-test-launcher-local:
	docker build -t ${TEST_LAUNCHER_IMG} -f dockerfiles/Dockerfile.launcher.cpu . --progress=plain --platform linux/$(shell go env GOARCH)

.PHONY: load-test-launcher-local
load-test-launcher-local:
	kind load docker-image ${TEST_LAUNCHER_IMG} --name ${CLUSTER_NAME}

.PHONY: build-populator-local
build-populator-local:
	KO_DOCKER_REPO=ko.local ko build -B ./cmd/launcher-populator -t ${POPULATOR_IMG_TAG} --platform linux/$(shell go env GOARCH)
	docker tag ko.local/launcher-populator:${POPULATOR_IMG_TAG} ${POPULATOR_IMG}

.PHONY: load-populator-local
load-populator-local:
	kind load docker-image ${POPULATOR_IMG} --name ${CLUSTER_NAME}

.PHONY: build-populator
build-populator:
	KO_DOCKER_REPO=$(CONTAINER_IMG_REG) ko build -B ./cmd/launcher-populator -t ${POPULATOR_IMG_TAG} --platform all

.PHONY: echo-var
echo-var:
	@echo "$($(VAR))"


## Location to install dependencies to
TOOLDIR ?= $(shell pwd)/hack/tools
TOOLBIN ?= $(TOOLDIR)/bin
$(TOOLBIN):
	mkdir -p $(TOOLBIN)

## Tools
CONTROLLER_GEN ?= $(TOOLBIN)/controller-gen
CONTROLLER_TOOLS_VERSION ?= v0.19.0
CONTROLLER_GEN_VERSION ?= $(CONTROLLER_GEN)-$(CONTROLLER_TOOLS_VERSION)

CODE_GEN_VERSION ?= v0.34.2
KUBE_CODEGEN_TAG := release-1.34
CODE_GEN_DIR ?= $(TOOLDIR)/code-generator-$(CODE_GEN_VERSION)
export CODE_GEN_DIR KUBE_CODEGEN_TAG

$(CONTROLLER_GEN_VERSION): $(TOOLBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

$(CODE_GEN_DIR):
	mkdir -p $(TOOLDIR)
	git clone -b $(CODE_GEN_VERSION) --depth 1 https://github.com/kubernetes/code-generator.git $(CODE_GEN_DIR)

.PHONY: manifests
manifests: $(CONTROLLER_GEN_VERSION) ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN_VERSION) crd paths=./api/...

.PHONY: generate
generate: $(CONTROLLER_GEN_VERSION) ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN_VERSION) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: generate_client
generate_client: $(CODE_GEN_DIR) ## (Re-)generate generated files
	./hack/generate-client.sh

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
set -e; \
if [ -z "$(1)" ]; then \
    echo "Error: Target path is empty but must not be"; \
    exit 1; \
fi; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(TOOLBIN) go install $${package} ;\
mv $(1) $(1)-$(3)
endef
