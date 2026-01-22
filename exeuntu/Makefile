default: build-exeuntu

ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

build-shelley: ## Download latest shelley binary from GitHub releases
	@echo "Downloading latest shelley for linux_$(ARCH)..."
	@curl -fsSL https://api.github.com/repos/boldsoftware/shelley/releases/latest \
		| jq -r '.assets[] | select(.name == "shelley_linux_$(ARCH)") | .browser_download_url' \
		| xargs curl -fsSL -o shelley
	@chmod +x shelley
	@echo "✓ Downloaded shelley"

build-exeuntu: build-shelley ## Build the exeuntu Docker image locally
	@echo "Building exeuntu Docker image..."
	docker build -t ghcr.io/boldsoftware/exeuntu:latest .
	@echo "✓ Image built locally as ghcr.io/boldsoftware/exeuntu:latest"

build: build-exeuntu

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
