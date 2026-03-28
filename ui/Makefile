.PHONY: build dev

# Local Node.js LTS — downloaded once into .node/
NODE_VERSION := 24.14.0
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

# In CI, CI_CACHE points to a persistent cache dir on the same filesystem as
# the build directory. Keep Node.js there so git-clean between builds is free.
# Locally, fall back to .node/ inside the working tree.
ifdef CI_CACHE
  NODE_DIR := $(CI_CACHE)/node
else
  NODE_DIR := .node
endif

NODE_BIN     := $(NODE_DIR)/node-v$(NODE_VERSION)-$(NODE_OS)-$(NODE_ARCH)/bin
PNPM         := $(NODE_BIN)/pnpm
export PATH  := $(CURDIR)/$(NODE_BIN):$(PATH)

# UI build cache: keyed on the git tree hash of ui/.
# Set UI_CACHE_DIR to override the default cache location.
UI_TREE_HASH := $(shell git rev-parse HEAD:ui/ 2>/dev/null)
ifdef UI_CACHE_DIR
  _UI_CACHE_BASE := $(UI_CACHE_DIR)
else ifdef CI_CACHE
  _UI_CACHE_BASE := $(CI_CACHE)/ui-dist-cache
else
  _UI_CACHE_BASE := .ui-cache
endif
_UI_CACHE_ENTRY := $(_UI_CACHE_BASE)/$(UI_TREE_HASH)

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

build: ## Build the Vue dashboard UI (with tree-hash cache)
	@if [ -n "$(UI_TREE_HASH)" ] && [ -d "$(_UI_CACHE_ENTRY)" ]; then \
		echo "UI cache hit ($(UI_TREE_HASH)), restoring dist/"; \
		rm -rf dist; \
		cp -a "$(_UI_CACHE_ENTRY)" dist; \
	else \
		$(MAKE) _build; \
		if [ -n "$(UI_TREE_HASH)" ]; then \
			echo "Caching UI build ($(UI_TREE_HASH))"; \
			mkdir -p "$(_UI_CACHE_BASE)"; \
			rm -rf "$(_UI_CACHE_ENTRY)"; \
			cp -a dist "$(_UI_CACHE_ENTRY)"; \
		fi; \
	fi

_build: $(NODE_BIN)/.corepack-enabled
	pnpm install --frozen-lockfile
	pnpm build
	@# Pre-compress assets for serving with Content-Encoding: gzip.
	@# Vite already content-hashes filenames, so .gz siblings are safe to cache.
	@# -n omits the timestamp so output is reproducible.
	find dist/assets -type f \( -name '*.js' -o -name '*.css' -o -name '*.svg' \) \
		| xargs -P0 gzip -9 -k -n

dev: $(NODE_BIN)/.corepack-enabled ## Run Vite dev server
	pnpm dev
