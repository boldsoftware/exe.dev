build-exeuntu: ## Build the exeuntu Docker image locally
	@echo "Building exeuntu Docker image..."
	@docker build -t ghcr.io/boldsoftware/exeuntu:latest ../exeuntu
	@echo "✓ Image built locally as ghcr.io/boldsoftware/exeuntu:latest"

push-exeuntu: build-exeuntu ## Build and push exeuntu image to GitHub Container Registry
	@echo "Pushing exeuntu image to GitHub Container Registry..."
	@echo "Note: You need to be logged in to ghcr.io first:"
	@echo "  echo \$$GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin"
	@docker push ghcr.io/boldsoftware/exeuntu:latest
	@echo "✓ Image pushed to ghcr.io/boldsoftware/exeuntu:latest"
