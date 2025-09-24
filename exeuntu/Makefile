default: build-exeuntu

build-exeuntu: ## Build the exeuntu Docker image locally
	@echo "Building exeuntu Docker image..."
	@cd .. && docker build -f exeuntu/Dockerfile -t ghcr.io/boldsoftware/exeuntu:latest .
	@echo "✓ Image built locally as ghcr.io/boldsoftware/exeuntu:latest"

push-local-exeuntu: build-exeuntu ## Build and push exeuntu image to local lima-exe-ctr
	@echo "Loading image into lima-exe-ctr..."
	@docker save ghcr.io/boldsoftware/exeuntu:latest | ssh lima-exe-ctr 'sudo nerdctl --namespace=exe load'

build: build-exeuntu

push-exeuntu: build-exeuntu ## Build and push exeuntu image to GitHub Container Registry
	@echo "Pushing exeuntu image to GitHub Container Registry..."
	@echo "Note: You need to be logged in to ghcr.io first:"
	@echo "  echo \$$GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin"
	@docker push ghcr.io/boldsoftware/exeuntu:latest
	@echo "✓ Image pushed to ghcr.io/boldsoftware/exeuntu:latest"

push: push-exeuntu push-exeuntu-welcome ## Build and push both images to GitHub Container Registry

run: build-exeuntu
	docker run -it \
	  --cap-add=ALL \
	  --security-opt seccomp=unconfined \
	  --security-opt apparmor=unconfined \
	  --cgroupns private \
	  --tmpfs /run \
	  --tmpfs /run/lock \
	  --tmpfs /tmp \
	  --tmpfs /sys/fs/cgroup:rw \
	  ghcr.io/boldsoftware/exeuntu:latest

run-bash: build-exeuntu
	docker run -it \
	  --cap-add=ALL \
	  --security-opt seccomp=unconfined \
	  --security-opt apparmor=unconfined \
	  --cgroupns private \
	  --tmpfs /run \
	  --tmpfs /run/lock \
	  --tmpfs /tmp \
	  --tmpfs /sys/fs/cgroup:rw \
	  ghcr.io/boldsoftware/exeuntu:latest bash
