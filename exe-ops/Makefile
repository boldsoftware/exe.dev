.PHONY: build build-agent build-server build-ui build-agent-embed test clean fmt run-server run-agent node

BIN_DIR := bin
TOKEN   ?= dev-token
DB      ?= exe-ops.db
ADDR    ?= :8080
SERVER  ?= http://localhost:8080
NAME    ?= $(shell hostname)
INTERVAL ?= 30s
TAGS    ?=

VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -X exe.dev/exe-ops/version.Version=$(VERSION) -X exe.dev/exe-ops/version.Commit=$(COMMIT) -X exe.dev/exe-ops/version.Date=$(DATE)

# Target architectures for agent binaries embedded in the server.
AGENT_TARGETS ?= linux-amd64

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

$(NPM):
	@mkdir -p $(NODE_DIR)
	@echo "Downloading Node.js v$(NODE_VERSION) ($(NODE_OS)-$(NODE_ARCH))..."
	@curl -fsSL $(NODE_URL) | tar -xJ -C $(NODE_DIR)
	@touch $@
	@echo "Node.js v$(NODE_VERSION) installed to $(NODE_BIN)"

node: $(NPM)
	@echo "node: $$($(NODE) --version)"
	@echo "npm:  $$($(NPM) --version)"
	@echo "PATH: $(NODE_BIN)"

build: build-agent build-server

build-agent:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/exe-ops-agent ./cmd/exe-ops-agent

build-agent-embed:
	@for target in $(AGENT_TARGETS); do \
		os=$${target%%-*}; \
		arch=$${target##*-}; \
		echo "Building agent for $$os/$$arch"; \
		mkdir -p server/agentbin/binaries/$$target; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o server/agentbin/binaries/$$target/exe-ops-agent ./cmd/exe-ops-agent; \
	done

build-server: build-ui build-agent-embed
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/exe-ops-server ./cmd/exe-ops-server

build-ui: $(NPM)
	cd ui && npm ci && npm run build

run-server: build-server
	$(BIN_DIR)/exe-ops-server --addr $(ADDR) --db $(DB) --token $(TOKEN) --tls=false

run-agent: build-agent
	$(BIN_DIR)/exe-ops-agent --server $(SERVER) --token $(TOKEN) --name $(NAME) --interval $(INTERVAL) $(if $(TAGS),--tags $(TAGS))

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
	rm -f server/agentbin/binaries/*/exe-ops-agent

clean-node:
	rm -rf $(NODE_DIR)
