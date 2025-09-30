CONTAINER_IMG_REG ?= my-registry/my-namespace
REQUESTER_IMG_REPO ?= requester
REQUESTER_IMG_TAG ?= latest
REQUESTER_IMAGE := $(CONTAINER_IMG_REG)/$(REQUESTER_IMG_REPO):$(REQUESTER_IMG_TAG)

CONTROLLER_IMG_TAG ?= $(shell git rev-parse --short HEAD)
CONTROLLER_IMG ?= $(CONTAINER_IMG_REG)/dual-pod-controller:$(CONTROLLER_IMG_TAG)

TARGETARCH ?= $(shell go env GOARCH)

.PHONY: build-requester
build-requester:
	docker build -t $(REQUESTER_IMAGE) -f dockerfiles/Dockerfile.requester . --progress=plain --platform=linux/$(TARGETARCH)

.PHONY: push-requester
push-requester:
	docker push $(REQUESTER_IMAGE)

.PHONY: build-controller-local
build-controller-local:
	KO_DOCKER_REPO=ko.local ko build -B ./cmd/dual-pods-controller -t ${CONTROLLER_IMG_TAG}
	docker tag ko.local/dual-pods-controller:${CONTROLLER_IMG_TAG} ${CONTROLLER_IMG}

.PHONY: build-controller
build-controller:
	KO_DOCKER_REPO=$(CONTAINER_IMG_REG) ko build -B ./cmd/dual-pods-controller -t ${CONTROLLER_IMG_TAG} --platform linux/amd64,linux/arm64

