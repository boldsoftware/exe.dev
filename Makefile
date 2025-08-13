# Makefile for exe.dev

# Variables
PROJECT_ID := exe-dev-468515
CLUSTER_NAME := exe-cluster
CLUSTER_LOCATION := us-west2-a
INSTANCE_NAME := exed-prod-01
TIMESTAMP := $(shell date +%Y%m%d-%H%M%S)
EXEUNTU_IMAGE := gcr.io/$(PROJECT_ID)/exeuntu

# Colors
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[1;33m
NC := \033[0m

.PHONY: help build test deploy setup-vm setup-dev clean run-dev image-build image-deploy image-size check-deploy

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*##"; printf "\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  ${GREEN}%-15s${NC} %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ''
	@echo 'Prerequisites for deployment commands:'
	@echo '  - gcloud CLI installed and authenticated (gcloud auth login)'
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

test-integration: ## Run integration tests (requires cluster access)
	@echo "Running integration tests..."
	@export GOOGLE_CLOUD_PROJECT=$(PROJECT_ID) && \
	export GKE_CLUSTER_NAME=$(CLUSTER_NAME) && \
	export GKE_CLUSTER_LOCATION=$(CLUSTER_LOCATION) && \
	go test ./container/... -v
	@echo "✓ Integration tests complete"

deploy: ## Deploy to production
	@echo "${YELLOW}Deploying to production...${NC}"
	@if ! command -v gcloud >/dev/null 2>&1; then \
		echo "${RED}Error: gcloud CLI not found${NC}"; \
		echo "${RED}Please install gcloud: https://cloud.google.com/sdk/docs/install${NC}"; \
		exit 1; \
	fi
	@if ! gcloud auth list --filter=status:ACTIVE --format="value(account)" | grep -q .; then \
		echo "${RED}Error: Not authenticated with gcloud${NC}"; \
		echo "${RED}Please run: gcloud auth login${NC}"; \
		exit 1; \
	fi
	@chmod +x deploy-binary.sh
	@./deploy-binary.sh

check-deploy: ## Check deployment prerequisites
	@echo "Checking deployment prerequisites..."
	@echo ""
	@if command -v gcloud >/dev/null 2>&1; then \
		echo "${GREEN}✓ gcloud CLI installed${NC}"; \
	else \
		echo "${RED}✗ gcloud CLI not found${NC}"; \
		echo "  Install: https://cloud.google.com/sdk/docs/install"; \
	fi
	@if gcloud auth list --filter=status:ACTIVE --format="value(account)" 2>/dev/null | grep -q .; then \
		ACCOUNT=$$(gcloud auth list --filter=status:ACTIVE --format="value(account)" | head -1); \
		echo "${GREEN}✓ Authenticated as $$ACCOUNT${NC}"; \
	else \
		echo "${RED}✗ Not authenticated with gcloud${NC}"; \
		echo "  Run: gcloud auth login"; \
	fi
	@if command -v tailscale >/dev/null 2>&1; then \
		echo "${GREEN}✓ Tailscale CLI installed${NC}"; \
		if tailscale status >/dev/null 2>&1; then \
			echo "${GREEN}✓ Tailscale is running${NC}"; \
			if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o BatchMode=yes "ubuntu@exed-prod-01" "echo 'test'" >/dev/null 2>&1; then \
				echo "${GREEN}✓ SSH access to exed-prod-01 working${NC}"; \
			else \
				echo "${YELLOW}⚠ SSH access to exed-prod-01 not working${NC}"; \
				echo "  Try: ssh ubuntu@exed-prod-01"; \
				echo "  Check: tailscale status | grep exed-prod-01"; \
			fi \
		else \
			echo "${YELLOW}⚠ Tailscale not running${NC}"; \
			echo "  Start Tailscale and connect to the network"; \
		fi \
	else \
		echo "${RED}✗ Tailscale not found${NC}"; \
		echo "  Install: https://tailscale.com/download"; \
	fi
	@echo ""
	@echo "If all items show ✓, you're ready to run: make deploy"

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

setup-dev: ## Enable development access to GKE cluster
	@echo "Enabling development access..."
	@chmod +x enable-dev-access.sh
	@./enable-dev-access.sh
	@echo "✓ Development access enabled"

run-dev: ## Run exed locally for development
	@echo "Starting development server..."
	@export GOOGLE_CLOUD_PROJECT=$(PROJECT_ID) && \
	export GKE_CLUSTER_NAME=$(CLUSTER_NAME) && \
	export GKE_CLUSTER_LOCATION=$(CLUSTER_LOCATION) && \
	export ENABLE_SANDBOX=true && \
	export STORAGE_CLASS_NAME=standard-rwo && \
	go run ./cmd/exed/exed.go -dev -http=:8080 -ssh=:2222

run-dev-bg: ## Run exed in background for development
	@echo "Starting development server in background..."
	@export GOOGLE_CLOUD_PROJECT=$(PROJECT_ID) && \
	export GKE_CLUSTER_NAME=$(CLUSTER_NAME) && \
	export GKE_CLUSTER_LOCATION=$(CLUSTER_LOCATION) && \
	export ENABLE_SANDBOX=true && \
	export STORAGE_CLASS_NAME=standard-rwo && \
	nohup go run ./cmd/exed/exed.go -dev -http=:8080 -ssh=:2222 > exed.log 2>&1 &
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

image-build: exeuntu/Dockerfile ## Build exeuntu Docker image locally
	@echo "${YELLOW}Building exeuntu image for amd64...${NC}"
	@cd exeuntu && docker build --platform linux/amd64 -t $(EXEUNTU_IMAGE):latest .
	@echo "${GREEN}✓ Image built: $(EXEUNTU_IMAGE):latest${NC}"

image-deploy: image-build ## Build and push exeuntu Docker image
	@echo "${YELLOW}Configuring Docker authentication for GCR...${NC}"
	@if ! command -v gcloud >/dev/null 2>&1; then \
		echo "${RED}Error: gcloud CLI not found${NC}"; \
		echo "${RED}Please install gcloud: https://cloud.google.com/sdk/docs/install${NC}"; \
		exit 1; \
	fi
	@if ! gcloud auth list --filter=status:ACTIVE --format="value(account)" | grep -q .; then \
		echo "${RED}Error: Not authenticated with gcloud${NC}"; \
		echo "${RED}Please run: gcloud auth login${NC}"; \
		exit 1; \
	fi
	@echo "Configuring Docker credential helper for GCR..."
	@gcloud auth configure-docker --quiet 2>/dev/null || true
	@echo "${YELLOW}Tagging and pushing exeuntu image...${NC}"
	@docker tag $(EXEUNTU_IMAGE):latest $(EXEUNTU_IMAGE):$(TIMESTAMP)
	@if ! docker push $(EXEUNTU_IMAGE):latest 2>/dev/null; then \
		echo "${RED}Error: Failed to push Docker image${NC}"; \
		echo "${RED}Please ensure you have push access to $(EXEUNTU_IMAGE)${NC}"; \
		echo "${RED}Try running: gcloud auth configure-docker${NC}"; \
		exit 1; \
	fi
	@docker push $(EXEUNTU_IMAGE):$(TIMESTAMP)
	@echo "${GREEN}✓ Image pushed to $(EXEUNTU_IMAGE):latest and $(EXEUNTU_IMAGE):$(TIMESTAMP)${NC}"

image-size: image-build ## Show the size of the exeuntu Docker image
	@echo "Checking exeuntu image size..."
	@docker images $(EXEUNTU_IMAGE):latest --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}" 2>/dev/null || \
	  (echo "${RED}Image $(EXEUNTU_IMAGE):latest not found. Run 'make image-build' first.${NC}" && exit 1)

.DEFAULT_GOAL := help
