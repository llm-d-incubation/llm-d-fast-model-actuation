ALLOCATOR_IMG_REG ?= my-registry/my-namespace
ALLOCATOR_IMG_REPO ?= gpu-allocator
ALLOCATOR_IMG_TAG ?= latest

ALLOCATOR_IMAGE := $(ALLOCATOR_IMG_REG)/$(ALLOCATOR_IMG_REPO):$(ALLOCATOR_IMG_TAG)

.PHONY: build-gpu-allocator
build-gpu-allocator:
	docker build -t $(ALLOCATOR_IMAGE) -f Dockerfile.gpu-allocator .

.PHONY: push-gpu-allocator
push-gpu-allocator:
	docker push $(ALLOCATOR_IMAGE)
