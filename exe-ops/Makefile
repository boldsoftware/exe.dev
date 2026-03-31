.DEFAULT_GOAL := build
.PHONY: build build-server build-ui test clean fmt run-server node

BIN_DIR := bin
ADDR    ?= :8080

VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -X exe.dev/exe-ops/version.Version=$(VERSION) -X exe.dev/exe-ops/version.Commit=$(COMMIT) -X exe.dev/exe-ops/version.Date=$(DATE)

# Local Node.js LTS — downloaded once into .node/
NODE_VERSION := 24.14.0
NODE_DIR     := .node
NODE_OS      := $(shell uname -s | tr A-Z a-z)
NODE_ARCH_RAW := $(shell uname -m)
ifeq ($(NODE_ARCH_RAW),x86_64)
  NODE_ARCH := x64
else ifeq ($(NODE_ARCH_RAW),aarch64)
  NODE_ARCH := arm64
else
  NODE_ARCH := $(NODE_ARCH_RAW)
endif
NODE_TARBALL := node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH).tar.xz
NODE_URL     := https://nodejs.org/dist/v$(NODE_VERSION)/$(NODE_TARBALL)
NODE_BIN     := $(NODE_DIR)/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)/bin
NPM          := $(NODE_BIN)/npm
NODE         := $(NODE_BIN)/node
export PATH  := $(CURDIR)/$(NODE_BIN):$(PATH)
# Ensure fully non-interactive builds (corepack download prompts, npm ci hints, etc.)
export COREPACK_ENABLE_AUTO_PIN := 0
export CI := 1

$(NODE_BIN)/.corepack-enabled:
	@mkdir -p $(NODE_DIR)
	@if [ ! -f "$@" ]; then \
		echo "Downloading Node.js v$(NODE_VERSION) ($(NODE_OS)-$(NODE_ARCH))..."; \
		tmp=$$(mktemp -d); \
		curl -fsSL $(NODE_URL) | tar -xJ -C "$$tmp"; \
		"$$tmp/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)/bin/corepack" enable \
			--install-directory "$$tmp/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)/bin"; \
		touch "$$tmp/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)/bin/.corepack-enabled"; \
		mv -Tf "$$tmp/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)" \
			"$(NODE_DIR)/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)" 2>/dev/null \
			|| mv -f "$$tmp/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)" \
				"$(NODE_DIR)/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)"; \
		rm -rf "$$tmp"; \
	fi
	@echo "Node.js v$(NODE_VERSION) ready in $(NODE_BIN)"

node: $(NODE_BIN)/.corepack-enabled
	@echo "node: $$($(NODE) --version)"
	@echo "npm:  $$($(NPM) --version)"
	@echo "PATH: $(NODE_BIN)"

build: build-server

build-server: build-ui
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/exe-ops-server ./cmd/exe-ops-server

build-ui: $(NODE_BIN)/.corepack-enabled
	cd ui && pnpm install --frozen-lockfile && pnpm run build

run-server: build-server
	$(BIN_DIR)/exe-ops-server --addr $(ADDR) --tls=false

test:
	go test ./...

fmt:
	goimports -w .
	gofmt -w .

clean:
	rm -rf $(BIN_DIR)
	rm -rf ui/dist
	mkdir -p ui/dist
	touch ui/dist/.gitkeep

clean-node:
	rm -rf $(NODE_DIR)
