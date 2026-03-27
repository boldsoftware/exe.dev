.PHONY: build dev

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
PNPM         := $(NODE_BIN)/pnpm
export PATH  := $(CURDIR)/$(NODE_BIN):$(PATH)

$(NODE_BIN)/.corepack-enabled:
	@mkdir -p $(NODE_DIR)
	@echo "Downloading Node.js v$(NODE_VERSION) ($(NODE_OS)-$(NODE_ARCH))..."
	@curl -fsSL $(NODE_URL) | tar -xJ -C $(NODE_DIR)
	@$(NODE_BIN)/corepack enable --install-directory $(NODE_BIN)
	@touch $@
	@echo "Node.js v$(NODE_VERSION) installed to $(NODE_BIN)"

build: $(NODE_BIN)/.corepack-enabled ## Build the Vue dashboard UI
	pnpm install --frozen-lockfile
	pnpm build

dev: $(NODE_BIN)/.corepack-enabled ## Run Vite dev server
	pnpm dev
