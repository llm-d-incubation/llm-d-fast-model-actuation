CONTAINER_IMG_REG ?= my-registry/my-namespace
REQUESTER_IMG_REPO ?= requester
REQUESTER_IMG_TAG ?= latest
REQUESTER_IMAGE := $(CONTAINER_IMG_REG)/$(REQUESTER_IMG_REPO):$(REQUESTER_IMG_TAG)

CONTROLLER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
CONTROLLER_IMG ?= $(CONTAINER_IMG_REG)/dual-pods-controller:$(CONTROLLER_IMG_TAG)
TEST_REQUESTER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
TEST_REQUESTER_IMG ?= $(CONTAINER_IMG_REG)/test-requester:$(TEST_REQUESTER_IMG_TAG)
TEST_SERVER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
TEST_SERVER_IMG ?= $(CONTAINER_IMG_REG)/test-server:$(TEST_SERVER_IMG_TAG)

TARGETARCH ?= $(shell go env GOARCH)

CLUSTER_NAME ?= fmatest

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

.PHONY: echo-var
echo-var:
	@echo "$($(VAR))"


## Location to install dependencies to
TOOLBIN ?= $(shell pwd)/hack/tools
$(TOOLBIN):
	mkdir -p $(TOOLBIN)

## Tool Binaries
CONTROLLER_GEN ?= $(TOOLBIN)/controller-gen
CONTROLLER_TOOLS_VERSION ?= v0.19.0
CONTROLLER_GEN_VERSION ?= $(CONTROLLER_GEN)-$(CONTROLLER_TOOLS_VERSION)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(TOOLBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN_VERSION) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN_VERSION) object:headerFile="hack/boilerplate.go.txt" paths="./..."


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
GOBIN=$(TOOLBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
}
endef
