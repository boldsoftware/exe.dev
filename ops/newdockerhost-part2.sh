#!/bin/bash
set -euo pipefail

echo "=== Installing Docker CE ==="

# Install prerequisites for Docker repository
sudo apt-get update
sudo apt-get install -y \
    ca-certificates \
    curl \
    gnupg \
    lsb-release

# Add Docker's official GPG key
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg

# Set up the Docker repository
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

# Install Docker Engine and buildx plugin
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

echo "=== Installing gVisor ==="

# Install gVisor prerequisites
sudo apt-get install -y \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg

# Add gVisor's GPG key
curl -fsSL https://gvisor.dev/archive.key | sudo gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg

# Add gVisor repository
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" | sudo tee /etc/apt/sources.list.d/gvisor.list > /dev/null

# Install runsc
sudo apt-get update && sudo apt-get install -y runsc

echo "=== Configuring Docker daemon ==="

# Create data directory if it doesn't exist
sudo mkdir -p /data/docker

# Configure Docker daemon with gVisor as default runtime
sudo tee /etc/docker/daemon.json > /dev/null <<'EOF'
{
    "default-runtime": "runsc",
    "data-root": "/data/docker",
    "icc": false,
    "iptables": true,
    "ip-forward": true,
    "ip-masq": true,
    "runtimes": {
        "runsc": {
            "path": "/usr/bin/runsc",
            "runtimeArgs": [
                "--net-raw"
            ]
        }
    }
}
EOF

# Stop and disable Docker daemon (it will be started by other machinery)
sudo systemctl stop docker
sudo systemctl disable docker
sudo systemctl disable containerd

echo "=== Docker daemon stopped and disabled ==="
echo "Docker is installed but not running. It will be started by separate machinery."

echo "=== Adding ubuntu user to docker group ==="

# Add ubuntu user to docker group
sudo usermod -aG docker ubuntu

echo "=== Setup complete ==="
echo "Note: You may need to log out and back in for docker group changes to take effect"
echo "Docker is installed but NOT running. Configuration:"
echo "  - gVisor (runsc) as default runtime"
echo "  - Data root at /data/docker"
echo "  - Inter-container communication disabled"
echo "  - Docker and containerd services are disabled"
echo "  - Will be started by separate machinery"