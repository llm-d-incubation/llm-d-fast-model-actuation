REQUESTER_IMG_REG ?= my-registry/my-namespace
REQUESTER_IMG_REPO ?= requester
REQUESTER_IMG_TAG ?= latest
REQUESTER_IMAGE := $(REQUESTER_IMG_REG)/$(REQUESTER_IMG_REPO):$(REQUESTER_IMG_TAG)

TARGETARCH ?= $(shell go env GOARCH)

.PHONY: build-requester
build-requester:
	docker build -t $(REQUESTER_IMAGE) -f dockerfiles/Dockerfile.requester . --progress=plain --platform=linux/$(TARGETARCH)
