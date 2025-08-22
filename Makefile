# Makefile for exe.dev

# Variables
INSTANCE_NAME := exed-prod-01
TIMESTAMP := $(shell date +%Y%m%d-%H%M%S)

# Colors
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[1;33m
NC := \033[0m

.PHONY: help build test deploy setup-vm clean run-dev

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

deploy: ## Deploy to production
	@echo "${YELLOW}Deploying to production...${NC}"
	@chmod +x deploy-binary.sh
	@./deploy-binary.sh

setup-vm: ## Set up production VM (run once) - requires TAILSCALE_AUTH_KEY
	@echo "Setting up production VM..."
	@if [ -z "$(TAILSCALE_AUTH_KEY)" ]; then \
		echo "ERROR: TAILSCALE_AUTH_KEY is required"; \
		echo "Usage: make setup-vm TAILSCALE_AUTH_KEY=tskey-auth-xxxx"; \
		echo ""; \
		echo "Get an auth key from: https://login.tailscale.com/admin/settings/keys"; \
		echo "Make sure to create a key with 'tag:server' tag"; \
		exit 1; \
	fi
	@chmod +x setup-production-vm.sh
	@./setup-production-vm.sh "$(TAILSCALE_AUTH_KEY)"
	@echo "✓ VM setup complete with Tailscale"


run-mdns: ## Run exed locally for development, and enable multicast DNS for *.exe.local resolution.
	@echo "Starting development server..."
	@echo "Note: Using ghcr.io/boldsoftware/exeuntu:latest image"
	@go run ./cmd/exed/exed.go -dev=local -http=0.0.0.0:8080 -ssh=0.0.0.0:2223 -mdns

run-dev: ## Run exed locally for development
	@echo "Starting development server..."
	@echo "Note: Using ghcr.io/boldsoftware/exeuntu:latest image"
	@go run ./cmd/exed/exed.go -dev=local -http=:8080 -ssh=:2223

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
	@rm -f exed exed.* *.log
	@go clean
	@echo "✓ Clean complete"

fmt: ## Format Go code
	@echo "Formatting code..."
	@go fmt ./...
	@echo "✓ Format complete"

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

container/rovol/arm64: Dockerfile.sshd
	@echo "Building SSH binaries for arm64..."
	@docker buildx build --platform linux/arm64 -f Dockerfile.sshd --target out --output type=local,dest=./container/rovol/arm64 .
	@echo "✓ Built container/rovol/arm64"

container/rovol/amd64: Dockerfile.sshd-amd64
	@echo "Building SSH binaries for amd64..."
	@docker buildx build --platform linux/amd64 -f Dockerfile.sshd-amd64 --target out --output type=local,dest=./container/rovol/amd64 .
	@echo "✓ Built container/rovol/amd64"

.DEFAULT_GOAL := help
