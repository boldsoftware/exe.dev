# Makefile for exe.dev

# Variables
ROOT_DIR := $(abspath $(lastword $(MAKEFILE_LIST))/..)
INSTANCE_NAME := exed-prod-01
TIMESTAMP := $(shell date +%Y%m%d-%H%M%S)
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
COMMIT := `git rev-parse --short HEAD`
VERSION := $(shell date +%Y%m%d)
REPO := exe.dev
DOCKER := docker

# Colors
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[1;33m
NC := \033[0m

.PHONY: help build test deploy-exed deploy-exed-staging deploy-exelet deploy-exelet-staging deploy-whoami deploy-what deploy-qa deploy-piperd deploy-piperd-staging clean run-dev generate whoami-clean ssh-exed-staging ssh-ctr-staging

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*##"; printf "\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  ${GREEN}%-15s${NC} %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ''
	@echo 'Prerequisites for deployment commands:'
	@echo '  - Tailscale installed and running (connects to production VM)'
	@echo '  - SSH key access to production VM via Tailscale'
	@echo '  - For first-time setup: TAILSCALE_AUTH_KEY from https://login.tailscale.com/admin/settings/keys'
	@echo ''

build: ## Build the exed binary
	@echo "Building exed binary..."
	@go build -ldflags="-s -w" -o exed ./cmd/exed/exed.go
	@echo "✓ Build complete"

test: ## Run all tests
	@echo "Running tests..."
	@go test ./... -v -short
	@echo "✓ Tests complete"

deploy-exed: ## Deploy exed to production
	@echo "${YELLOW}Deploying exed to production...${NC}"
	@chmod +x ops/deploy/deploy-exed-prod.sh
	@./ops/deploy/deploy-exed-prod.sh
	@./scripts/discord-notify.sh "deployed $(shell git rev-parse --short HEAD)"

deploy-exed-staging: ## Deploy exed to staging
	@echo "${YELLOW}Deploying exed to staging...${NC}"
	@chmod +x ops/deploy/deploy-exed-staging.sh
	@./ops/deploy/deploy-exed-staging.sh

deploy-exelet: ## Deploy exelet to production
	@echo "${YELLOW}Deploying exelet to production...${NC}"
	@chmod +x ops/deploy/deploy-exelet-prod.sh
	@./ops/deploy/deploy-exelet-prod.sh

deploy-exelet-staging: ## Deploy exelet to staging
	@echo "${YELLOW}Deploying exelet to staging...${NC}"
	@chmod +x ops/deploy/deploy-exelet-staging.sh
	@./ops/deploy/deploy-exelet-staging.sh

deploy-whoami: ## Deploy whoami sqlite database to production
	@echo "${YELLOW}Deploying whoami database to production...${NC}"
	@chmod +x ops/deploy-whoami.sh
	@./ops/deploy-whoami.sh

ssh-exed: ## ssh to exed production server
	@ssh ubuntu@exed-02

ssh-ci: ## ssh to exed ci server
	@ssh root@ci.bold.dev

ssh-ctr: ## ssh to ctr-host
	@ssh ubuntu@exe-ctr-02

ssh-exed-staging: ## ssh to exed staging server
	@ssh ubuntu@exed-staging-01

ssh-ctr-staging: ## ssh to ctr-host staging
	@ssh ubuntu@exe-ctr-staging-01

ssh-mon: ## ssh to monitoring (prometheus/grafana) server
	@ssh ubuntu@mon

deploy-piperd: ## Deploy sshpiperd to production
	@echo "${YELLOW}Deploying sshpiperd to production...${NC}"
	@chmod +x ops/deploy/deploy-sshpiper-prod.sh
	@./ops/deploy/deploy-sshpiper-prod.sh

deploy-piperd-staging: ## Deploy sshpiperd to staging
	@echo "${YELLOW}Deploying sshpiperd to staging...${NC}"
	@chmod +x ops/deploy/deploy-sshpiper-staging.sh
	@./ops/deploy/deploy-sshpiper-staging.sh

deploy-what: ## Show commits that would deploy to production
	@./ops/deploy-what.sh

deploy-qa: ## Ask codex for a QA/testing plan for pending changes
	@./ops/deploy-qa.sh

run-dev: ## Run exed locally for development
	@echo "Starting dev server with ghcr.io/boldsoftware/exeuntu:latest"
	@go run ./cmd/exed/exed.go -dev=local -http=:8080 -ssh=:2223

run-devlet: ## Run exed locally for development along with exelet
	@echo "Starting dev server with ghcr.io/boldsoftware/exeuntu:latest"
	@LOG_LEVEL=debug go run ./cmd/exed/exed.go -dev=local -http=:8080 -ssh=:2223 -start-exelet

run-sshpiper: ## Run sshpiper proxy server
	@echo "Starting sshpiper proxy..."
	@./sshpiper.sh

run-dev-bg: ## Run exed in background for development
	@echo "Starting development server in background..."
	@nohup go run ./cmd/exed/exed.go -dev=local -http=:8080 -ssh=:2223 > exed.log 2>&1 &
	@echo "✓ Server started in background. Check exed.log for output"
	@echo "To stop: make stop-dev"

stop-dev: ## Stop development server
	@echo "Stopping development server..."
	@pkill -f "go run ./cmd/exed/exed.go" || true
	@echo "✓ Server stopped"

ssh-vm: ## SSH to production VM
	@echo "Connecting to production VM via Tailscale..."
	@ssh ubuntu@exed-prod-01

logs: ## View production logs
	@echo "Fetching production logs..."
	@ssh ubuntu@exed-prod-01 'sudo tail -f /var/log/exed/exed.log'

logs-error: ## View production error logs
	@echo "Fetching production error logs..."
	@ssh ubuntu@exed-prod-01 'sudo tail -f /var/log/exed/exed.error.log'

status: ## Check production service status
	@echo "Checking production service status..."
	@ssh ubuntu@exed-prod-01 'sudo systemctl status exed --no-pager'

restart: ## Restart production service
	@echo "Restarting production service..."
	@ssh ubuntu@exed-prod-01 'sudo systemctl restart exed && echo "✓ Service restarted"'

clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -f exed exed.* *.log exeletd exelet-ctl
	@go clean
	@echo "✓ Clean complete"

rmdb: ## Remove local exed database
	@rm -f exe.db*

fmt: ## Format Go code
	@echo "Formatting code..."
	@go fmt ./...
	@echo "✓ Format complete"

generate: ## Run go generate
	@echo "Running go generate..."
	@go generate ./...

vet: ## Run go vet
	@echo "Running go vet..."
	@go vet ./...
	@echo "✓ Vet complete"

lint: ## Run linters
	@echo "Running linters..."
	@go vet ./...
	@go fmt ./...
	@echo "✓ Lint complete"

sshd: container/rovol/arm64 container/rovol/amd64

container/rovol/arm64: container/rovol/Dockerfile.rovol
	@echo "Building SSH binaries for arm64..."
	@rm -rf ./container/rovol/arm64
	@mkdir -p ./container/rovol/arm64
	@$(DOCKER) buildx build --platform linux/arm64 -f container/rovol/Dockerfile.rovol --target out --output type=local,dest=./container/rovol/arm64 .
	@echo "✓ Built container/rovol/arm64"

container/rovol/amd64: container/rovol/Dockerfile.rovol
	@echo "Building SSH binaries for amd64..."
	@rm -rf ./container/rovol/amd64
	@mkdir -p ./container/rovol/amd64
	@$(DOCKER) buildx build --platform linux/amd64 -f container/rovol/Dockerfile.rovol --target out --output type=local,dest=./container/rovol/amd64 .
	@echo "✓ Built container/rovol/amd64"

whoami: ## Download ghuser/whoami.sqlite3 from Backblaze if it doesn't exist
	@if [ ! -f ghuser/whoami.sqlite3 ]; then \
		if ! command -v b2 >/dev/null 2>&1; then \
			echo "${RED}Error: b2 command not found${NC}"; \
			echo "Please install the Backblaze B2 CLI (e.g. brew install b2-tools)"; \
			exit 1; \
		fi; \
		if ! command -v zstd >/dev/null 2>&1; then \
			echo "${RED}Error: zstd command not found${NC}"; \
			echo "Please install zstd (e.g. brew install zstd)"; \
			exit 1; \
		fi; \
		echo "Downloading ghuser/whoami.sqlite3 from Backblaze..."; \
		export B2_APPLICATION_KEY_ID="004edb881590a7d0000000008"; \
		export B2_APPLICATION_KEY="K004hvv/i5raZbvKXARk+H7sZLZ5XtQ"; \
		export COLUMNS="$${COLUMNS:-80}"; \
		export LINES="$${LINES:-24}"; \
		b2 account authorize >/dev/null 2>&1 && \
		b2 file download b2://bold-exe/whoami3.sqlite3.zst ghuser/whoami.sqlite3.zst \
			|| { echo "${RED}Failed to download whoami.sqlite3.zst${NC}" && exit 1; }; \
		echo "Decompressing ghuser/whoami.sqlite3.zst..."; \
		zstd -d ghuser/whoami.sqlite3.zst -o ghuser/whoami.sqlite3 && \
		rm ghuser/whoami.sqlite3.zst; \
		echo "✓ Downloaded and decompressed ghuser/whoami.sqlite3"; \
	else \
		echo "✓ ghuser/whoami.sqlite3 already exists"; \
	fi

whoami-clean: ## Remove ghuser/whoami.sqlite3 so it can be re-downloaded
	@if [ -f ghuser/whoami.sqlite3 ]; then \
		echo "Removing ghuser/whoami.sqlite3..."; \
		rm ghuser/whoami.sqlite3; \
		echo "✓ Removed ghuser/whoami.sqlite3"; \
	else \
		echo "${RED}Error: ghuser/whoami.sqlite3 not found${NC}"; \
		echo "Run 'make ghuser/whoami' to download it."; \
		exit 1; \
	fi

exelet-fs: ## Download exelet-fs from Backblaze if it doesn't exist
	@if [ ! -e exelet/fs/kernel ] || [ ! -e exelet/fs/rovol ]; then \
		if ! command -v b2 >/dev/null 2>&1; then \
			echo "${RED}Error: b2 command not found${NC}"; \
			echo "Please install the Backblaze B2 CLI (e.g. brew install b2-tools)"; \
			exit 1; \
		fi; \
		echo "Downloading exelet-fs from Backblaze..."; \
		export B2_APPLICATION_KEY_ID="004edb881590a7d0000000008"; \
		export B2_APPLICATION_KEY="K004hvv/i5raZbvKXARk+H7sZLZ5XtQ"; \
		export COLUMNS="$${COLUMNS:-80}"; \
		export LINES="$${LINES:-24}"; \
		b2 account authorize >/dev/null 2>&1 && \
		b2 file download b2://bold-exe/exelet-fs-$(GOARCH).tar.gz .exelet-fs.tar.gz \
			|| { echo "${RED}Failed to download exelet-fs-$(GOARCH).tar.gz ${NC}" && exit 1; }; \
		echo "Decompressing exelet-fs..."; \
		tar zxf .exelet-fs.tar.gz -C exelet/fs && \
		rm .exelet-fs.tar.gz; \
		echo "✓ Downloaded and decompressed exelet-fs"; \
	fi

.PHONY: protos
protos:
	@$(DOCKER) buildx build -f ./Dockerfile.protobuf --output type=local,dest=pkg .

.PHONY: exelet
exelet: exelet-fs
	@>&2 echo " -> building exelet ${COMMIT}${BUILD}"
	@# exelet only runs in linux
	@GOOS=linux go build -ldflags="-s -w" -o exeletd ./cmd/exelet

.PHONY: exelet-coverage
exelet-coverage: exelet-kernel exelet-rovol
	@>&2 echo " -> building exelet with coverage ${COMMIT}${BUILD}"
	@# exelet only runs in linux
	@cd ./cmd/exelet && GOOS=linux go build -cover -covermode=atomic -coverpkg=exe.dev/... -mod=mod -installsuffix cgo -ldflags "-w -X $(REPO)/version.Commit=$(COMMIT) -X $(REPO)/version.Version=$(VERSION) -X $(REPO)/version.Build=$(BUILD)" -o $(ROOT_DIR)/exeletd .

.PHONY: exelet-ctl
exelet-ctl:
	@>&2 echo " -> building exelet-ctl ${COMMIT}${BUILD} (${GOOS}/${GOARCH})"
	@cd ./cmd/exelet-ctl && go build -mod=mod -installsuffix cgo -ldflags "-w -X $(REPO)/version.Commit=$(COMMIT) -X $(REPO)/version.Version=$(VERSION) -X $(REPO)/version.Build=$(BUILD)" -o $(ROOT_DIR)/exelet-ctl .

.PHONY: exe-init
exe-init:
	@>&2 echo " -> building exe-init ${COMMIT}${BUILD}"
	@# exelet only runs in linux
	@cd ./cmd/exe-init && CGO_ENABLED=0 GOOS=linux go build -mod=mod -tags osusergo,netgo -ldflags "-extldflags=-static -w -X $(REPO)/version.Commit=$(COMMIT) -X $(REPO)/version.Version=$(VERSION) -X $(REPO)/version.Build=$(BUILD)" -o $(ROOT_DIR)/exelet/fs/rovol/bin/exe-init .

.PHONY: exe-ssh
exe-ssh:
	@>&2 echo " -> building exe-ssh ${COMMIT}${BUILD} (${GOOS}/${GOARCH})"
	@cd ./cmd/exe-ssh && CGO_ENABLED=0 go build -mod=mod -tags osusergo,netgo -ldflags "-extldflags=-static -w -X $(REPO)/version.Commit=$(COMMIT) -X $(REPO)/version.Version=$(VERSION) -X $(REPO)/version.Build=$(BUILD)" -o $(ROOT_DIR)/exelet/fs/rovol/bin/exe-ssh .

# kernel
exelet-kernel: exelet/fs/kernel/kernel
exelet/fs/kernel/kernel:
	@>&2 echo " -> building exelet kernel"
	@mkdir -p exelet/fs/kernel
	@$(DOCKER) buildx build --platform linux/$(GOARCH) $(BUILD_ARGS) --output type=local,dest=./exelet/fs/kernel/ -f ./exelet/kernel/Dockerfile ./exelet/kernel

# exelet rovol
exelet-rovol: exelet/fs/rovol
exelet/fs/rovol:
	@>&2 echo " -> building exelet rovol"
	@mkdir -p exelet/fs/rovol
	@$(DOCKER) buildx build --platform linux/$(GOARCH) $(BUILD_ARGS) --output type=local,dest=./exelet/fs/rovol -f ./exelet/rovol/Dockerfile .

package-exelet-fs:
	@rm -rf /tmp/exelet-fs
	@mkdir -p /tmp/exelet-fs
	@>&2 echo " -> building exelet kernel"
	@docker buildx build --platform linux/$(GOARCH) $(BUILD_ARGS) --output type=local,dest=/tmp/exelet-fs/kernel/ -f ./exelet/kernel/Dockerfile ./exelet/kernel
	@>&2 echo " -> building exelet rovol"
	@docker buildx build --platform linux/$(GOARCH) $(BUILD_ARGS) --output type=local,dest=/tmp/exelet-fs/rovol/ -f ./exelet/rovol/Dockerfile .
	@>&2 echo " -> building exelet rovol"
	@cd ./cmd/exe-init && CGO_ENABLED=0 GOOS=linux go build -mod=mod -tags osusergo,netgo -ldflags "-extldflags=-static -w -X $(REPO)/version.Commit=$(COMMIT) -X $(REPO)/version.Version=$(VERSION) -X $(REPO)/version.Build=$(BUILD)" -o /tmp/exelet-fs/rovol/bin/exe-init .
	@cd /tmp/exelet-fs && tar czvf $(ROOT_DIR)/exelet-fs-$(GOARCH).tar.gz ./

package-exelet-fs:
	@rm -rf /tmp/exelet-fs
	@mkdir -p /tmp/exelet-fs
	@>&2 echo " -> building exelet kernel"
	@docker buildx build --platform linux/$(GOARCH) $(BUILD_ARGS) --output type=local,dest=/tmp/exelet-fs/kernel/ -f ./exelet/kernel/Dockerfile ./exelet/kernel
	@>&2 echo " -> building exelet rovol"
	@docker buildx build --platform linux/$(GOARCH) $(BUILD_ARGS) --output type=local,dest=/tmp/exelet-fs/rovol/ -f ./exelet/rovol/Dockerfile .
	@>&2 echo " -> building exelet rovol"
	@cd ./cmd/exe-init && CGO_ENABLED=0 GOOS=linux go build -mod=mod -tags osusergo,netgo -ldflags "-extldflags=-static -w -X $(REPO)/version.Commit=$(COMMIT) -X $(REPO)/version.Version=$(VERSION) -X $(REPO)/version.Build=$(BUILD)" -o /tmp/exelet-fs/rovol/bin/exe-init .
	@cd /tmp/exelet-fs && tar czvf $(ROOT_DIR)/exelet-fs-$(GOARCH).tar.gz ./

.DEFAULT_GOAL := help
