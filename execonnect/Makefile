GO ?= go
DOCKER ?= docker
OUTPUT ?= bin/execonnect

.PHONY: all build build-all check proto protos test vet

all: build

build:
	@mkdir -p "$(dir $(OUTPUT))"
	@CGO_ENABLED=0 $(GO) build -trimpath -o "$(OUTPUT)" ./cmd/execonnect

build-all:
	@set -e; \
	out=$$(mktemp -d); \
	trap 'rm -rf "$$out"' EXIT; \
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		goos=$${target%/*}; \
		goarch=$${target#*/}; \
		echo "building $$goos/$$goarch"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -o "$$out/execonnect-$$goos-$$goarch" ./cmd/execonnect; \
	done

check: test vet build-all

test:
	@$(GO) test ./...

vet:
	@$(GO) vet ./...

proto: protos

protos:
	@DOCKER="$(DOCKER)" bash ./scripts/build-protos.sh
