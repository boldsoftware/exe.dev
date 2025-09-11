default: build-exeuntu build-exeuntu-welcome

build-exeuntu: ## Build the exeuntu Docker image locally
	@echo "Building exeuntu Docker image..."
	@docker build -t ghcr.io/boldsoftware/exeuntu:latest .
	@echo "✓ Image built locally as ghcr.io/boldsoftware/exeuntu:latest"

build-exeuntu-welcome: build-exeuntu ## Build the exeuntu-welcome Docker image locally
	@echo "Building exeuntu-welcome Docker image..."
	@docker build -f Dockerfile.welcome -t ghcr.io/boldsoftware/exeuntu-welcome:latest .
	@echo "✓ Image built locally as ghcr.io/boldsoftware/exeuntu-welcome:latest"

build: build-exeuntu build-exeuntu-welcome ## Build both Docker images

push-exeuntu: build-exeuntu ## Build and push exeuntu image to GitHub Container Registry
	@echo "Pushing exeuntu image to GitHub Container Registry..."
	@echo "Note: You need to be logged in to ghcr.io first:"
	@echo "  echo \$$GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin"
	@docker push ghcr.io/boldsoftware/exeuntu:latest
	@echo "✓ Image pushed to ghcr.io/boldsoftware/exeuntu:latest"

push-exeuntu-welcome: build-exeuntu-welcome ## Build and push exeuntu-welcome image to GitHub Container Registry
	@echo "Pushing exeuntu-welcome image to GitHub Container Registry..."
	@echo "Note: You need to be logged in to ghcr.io first:"
	@echo "  echo \$$GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin"
	@docker push ghcr.io/boldsoftware/exeuntu-welcome:latest
	@echo "✓ Image pushed to ghcr.io/boldsoftware/exeuntu-welcome:latest"

push: push-exeuntu push-exeuntu-welcome ## Build and push both images to GitHub Container Registry
