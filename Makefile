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
