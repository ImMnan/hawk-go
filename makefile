APP_NAME := hawk-go
VERSION := 0.5.1
REGISTRY := immnan
IMAGE := $(REGISTRY)/hawk

GOOS := linux
GOARCH := amd64

BINARY_DIR := bin
BINARY := $(BINARY_DIR)/$(APP_NAME)-$(GOOS)-$(GOARCH)
DOCKERFILE := ContainerFiles/ContainerFile

.PHONY: help build-binary build-image push-image release clean

help:
	@echo "Available targets:"
	@echo "  make build-binary  - Build $(GOOS)/$(GOARCH) Go binary"
	@echo "  make build-image   - Build container image and tag $(IMAGE):$(VERSION), $(IMAGE):latest"
	@echo "  make push-image    - Push both image tags to Docker registry"
	@echo "  make release       - Build image and push both tags"
	@echo "  make clean         - Remove local build artifacts"

build-binary:
	@mkdir -p $(BINARY_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BINARY) ./main.go
	@echo "Built binary: $(BINARY)"

build-image:
	docker buildx build \
		--platform $(GOOS)/$(GOARCH) \
		--load \
		-f $(DOCKERFILE) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

push-image:
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

release: build-image push-image

clean:
	rm -rf $(BINARY_DIR)
