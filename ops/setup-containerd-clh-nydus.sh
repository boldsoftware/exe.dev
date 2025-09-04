#!/bin/bash
set -euo pipefail

echo "=== Starting clean setup for $(hostname) with Cloud Hypervisor + Nydus ==="

# Prevent service restarts during package installation that could kill SSH/Tailscale
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# Setup 500GB swap on each NVMe drive with equal priority for I/O interleaving
echo "=== Setting up dual swap partitions on NVMe drives ==="

# First NVMe drive
NVME1="/dev/nvme0n1"
echo "Setting up 500GB swap on ${NVME1}..."
sudo parted -s ${NVME1} mklabel gpt
sudo parted -s ${NVME1} mkpart primary linux-swap 1MiB 501GiB
sudo mkswap ${NVME1}p1

# Second NVMe drive
NVME2="/dev/nvme1n1"
echo "Setting up 500GB swap on ${NVME2}..."
sudo parted -s ${NVME2} mklabel gpt
sudo parted -s ${NVME2} mkpart primary linux-swap 1MiB 501GiB
sudo mkswap ${NVME2}p1

# Enable both swaps with equal priority for I/O interleaving
sudo swapon -p 1 ${NVME1}p1
sudo swapon -p 1 ${NVME2}p1

# Add to fstab with priority
echo "${NVME1}p1 none swap sw,pri=1 0 0" | sudo tee -a /etc/fstab
echo "${NVME2}p1 none swap sw,pri=1 0 0" | sudo tee -a /etc/fstab

echo "Dual swap setup complete (2x 500GB with equal priority)"

# Setup data volume
echo "=== Setting up data volume ==="
DATA_DEVICE=""

# First check if xvdf exists (non-metal instances)
if [ -e /dev/xvdf ]; then
    DATA_DEVICE="/dev/xvdf"
else
    # On metal instances, find the 250GB NVMe device
    echo "Looking for 250GB NVMe data volume..."
    for nvme in /dev/nvme*n1; do
        if [ -b "$nvme" ]; then
            SIZE_HR=$(lsblk -n -d -o SIZE "$nvme" 2>/dev/null | tr -d ' ')
            echo "Checking NVMe device $nvme with size ${SIZE_HR}"
            
            SIZE_GB=$(lsblk -b -n -d -o SIZE "$nvme" 2>/dev/null | awk '{printf "%.0f", $1/1073741824}')
            
            if [ -n "$SIZE_GB" ] && [ "$SIZE_GB" -ge 245 ] && [ "$SIZE_GB" -le 255 ]; then
                DATA_DEVICE="$nvme"
                echo "Found data volume at $DATA_DEVICE (${SIZE_GB}GB)"
                break
            fi
        fi
    done
fi

if [ -z "$DATA_DEVICE" ]; then
    echo "ERROR: Could not find data volume (250GB device)"
    echo "Available block devices:"
    lsblk
    exit 1
fi

echo "Using data device: $DATA_DEVICE"
sudo mkfs.xfs $DATA_DEVICE
sudo mkdir -p /data
sudo mount -o pquota $DATA_DEVICE /data
echo "$DATA_DEVICE /data xfs defaults,pquota 0 0" | sudo tee -a /etc/fstab
sudo xfs_quota -x -c 'state' /data
echo "Data volume setup complete"

echo "=== Installing containerd ==="

# Install prerequisites
sudo DEBIAN_FRONTEND=noninteractive apt-get update
sudo DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 apt-get install -y -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" \
    ca-certificates \
    curl \
    gnupg \
    lsb-release \
    apt-transport-https \
    jq \
    build-essential \
    pkg-config \
    libseccomp-dev \
    wget \
    skopeo

# Install containerd from official releases (not apt) for specific version
CONTAINERD_VERSION="2.1.4"
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    ARCH="amd64"
elif [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
    ARCH="arm64"
fi

# Download and install containerd
wget -q https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz
sudo tar -xzf containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz -C /usr/local
rm containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz

# Install containerd systemd service
sudo mkdir -p /usr/local/lib/systemd/system
sudo curl -L https://raw.githubusercontent.com/containerd/containerd/main/containerd.service -o /usr/local/lib/systemd/system/containerd.service
sudo systemctl daemon-reload

# Install runc
RUNC_VERSION="1.1.14"
sudo wget -q https://github.com/opencontainers/runc/releases/download/v${RUNC_VERSION}/runc.${ARCH} -O /usr/local/sbin/runc
sudo chmod +x /usr/local/sbin/runc

echo "=== Installing Kata Containers with Cloud Hypervisor ==="

KATA_VERSION="3.20.0"
if [ "$ARCH" = "amd64" ]; then
    KATA_ARCH="x86_64"
else
    KATA_ARCH="$ARCH"
fi

# Download and install Kata
KATA_URL="https://github.com/kata-containers/kata-containers/releases/download/${KATA_VERSION}/kata-static-${KATA_VERSION}-${KATA_ARCH}.tar.xz"
echo "Downloading Kata from: $KATA_URL"
wget -q $KATA_URL -O kata-static.tar.xz
sudo tar -xf kata-static.tar.xz -C /
rm kata-static.tar.xz

# Ensure Cloud Hypervisor is executable
sudo chmod +x /opt/kata/bin/cloud-hypervisor
sudo chmod +x /opt/kata/bin/containerd-shim-kata-v2

# Link Kata binaries
sudo ln -sf /opt/kata/bin/kata-runtime /usr/local/bin/kata-runtime
sudo ln -sf /opt/kata/bin/containerd-shim-kata-v2 /usr/local/bin/containerd-shim-kata-v2

echo "=== Installing Nydus Snapshotter ==="

# Install nydus-snapshotter and nydusd daemon
NYDUS_VERSION="0.15.2"
NYDUSD_VERSION="2.2.5"
if [ "$ARCH" = "amd64" ]; then
    NYDUS_ARCH="x86_64"
else
    NYDUS_ARCH="$ARCH"
fi

# Download and install nydus-snapshotter
echo "Installing nydus-snapshotter v${NYDUS_VERSION}..."
wget -q https://github.com/containerd/nydus-snapshotter/releases/download/v${NYDUS_VERSION}/nydus-snapshotter-v${NYDUS_VERSION}-linux-${NYDUS_ARCH}.tar.gz
# Extract to temp dir first since binaries are in bin/ subdirectory
mkdir -p /tmp/nydus-extract
tar -xzf nydus-snapshotter-v${NYDUS_VERSION}-linux-${NYDUS_ARCH}.tar.gz -C /tmp/nydus-extract
sudo mv /tmp/nydus-extract/bin/* /usr/local/bin/
rm -rf /tmp/nydus-extract nydus-snapshotter-v${NYDUS_VERSION}-linux-${NYDUS_ARCH}.tar.gz
sudo chmod +x /usr/local/bin/containerd-nydus-grpc

# Download and install nydusd daemon
echo "Installing nydusd daemon v${NYDUSD_VERSION}..."
wget -q https://github.com/dragonflyoss/nydus/releases/download/v${NYDUSD_VERSION}/nydus-static-v${NYDUSD_VERSION}-linux-${NYDUS_ARCH}.tgz
tar -xzf nydus-static-v${NYDUSD_VERSION}-linux-${NYDUS_ARCH}.tgz
sudo cp nydus-static/nydusd* /usr/local/bin/
sudo cp nydus-static/nydus-image /usr/local/bin/
sudo chmod +x /usr/local/bin/nydusd* /usr/local/bin/nydus-image
rm -rf nydus-static nydus-static-v${NYDUSD_VERSION}-linux-${NYDUS_ARCH}.tgz

# Create nydus configuration directory
sudo mkdir -p /etc/nydus
# Create proper nydusd configuration
cat <<'EOF' | sudo tee /etc/nydus/nydusd-config.json > /dev/null
{
  "device": {
    "backend": {
      "type": "registry",
      "config": {
        "scheme": "https",
        "skip_verify": false,
        "timeout": 5,
        "connect_timeout": 5,
        "retry_limit": 2
      }
    },
    "cache": {
      "type": "blobcache",
      "config": {
        "work_dir": "/var/lib/nydus/cache"
      }
    }
  },
  "mode": "direct",
  "digest_validate": false,
  "iostats_files": false
}
EOF

# Create nydus working directories
sudo mkdir -p /var/lib/nydus/cache
sudo mkdir -p /var/lib/containerd-nydus
sudo mkdir -p /run/containerd-nydus

# Create systemd service for nydus-snapshotter
cat <<'EOF' | sudo tee /etc/systemd/system/nydus-snapshotter.service > /dev/null
[Unit]
Description=Nydus snapshotter for containerd
After=network.target containerd.service
Wants=containerd.service

[Service]
Type=simple
Environment="CONTAINERD_ADDRESS=/run/containerd/containerd.sock"
ExecStart=/usr/local/bin/containerd-nydus-grpc \
    --nydusd-config=/etc/nydus/nydusd-config.json \
    --log-level=info \
    --log-to-stdout \
    --root=/var/lib/containerd-nydus \
    --address=/run/containerd-nydus/containerd-nydus-grpc.sock
Restart=always
RestartSec=5
KillMode=process

[Install]
WantedBy=multi-user.target
EOF

echo "=== Configuring Kata for Cloud Hypervisor with virtio-fs-nydus ==="

# Copy the default Cloud Hypervisor configuration and customize it
sudo mkdir -p /etc/kata-containers
sudo cp /opt/kata/share/defaults/kata-containers/configuration-clh.toml /etc/kata-containers/configuration-clh.toml

# Create a symlink for default kata configuration to use CLH
sudo rm -f /etc/kata-containers/configuration.toml
sudo ln -s /etc/kata-containers/configuration-clh.toml /etc/kata-containers/configuration.toml

# Also update the default in /opt/kata to use CLH instead of QEMU
sudo rm -f /opt/kata/share/defaults/kata-containers/configuration.toml
sudo ln -s /etc/kata-containers/configuration-clh.toml /opt/kata/share/defaults/kata-containers/configuration.toml

echo "=== Configuring containerd with Nydus snapshotter ==="

# Create data directory
sudo mkdir -p /data/containerd

# Configure containerd with nydus as default snapshotter
sudo mkdir -p /etc/containerd
cat <<'EOF' | sudo tee /etc/containerd/config.toml > /dev/null
version = 2
root = "/data/containerd"

[grpc]
  address = "/run/containerd/containerd.sock"

[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    sandbox_image = "registry.k8s.io/pause:3.9"
    
    # Use nydus as the default snapshotter
    [plugins."io.containerd.grpc.v1.cri".containerd]
      snapshotter = "nydus"
      disable_snapshot_annotations = false
      default_runtime_name = "kata"
      
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes]
        [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
          runtime_type = "io.containerd.runc.v2"
          [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
            SystemdCgroup = true
        
        [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.kata]
          runtime_type = "io.containerd.kata.v2"
          privileged_without_host_devices = true
          pod_annotations = ["io.katacontainers.*"]
          snapshotter = "nydus"
          
          [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.kata.options]
            ConfigPath = "/etc/kata-containers/configuration-clh.toml"
    
    [plugins."io.containerd.grpc.v1.cri".cni]
      bin_dir = "/opt/cni/bin"
      conf_dir = "/etc/cni/net.d"

[proxy_plugins]
  [proxy_plugins.nydus]
    type = "snapshot"
    address = "/run/containerd-nydus/containerd-nydus-grpc.sock"

[plugins."io.containerd.internal.v1.opt"]
  path = "/opt/containerd"

[plugins."io.containerd.metadata.v1.bolt"]
  content_sharing_policy = "shared"
EOF
sudo cp /etc/containerd/config.toml /etc/containerd/config.toml.exedev

echo "=== Installing nerdctl ==="

# Install nerdctl for easier container management
NERDCTL_VERSION="1.7.7"
wget -q https://github.com/containerd/nerdctl/releases/download/v${NERDCTL_VERSION}/nerdctl-${NERDCTL_VERSION}-linux-${ARCH}.tar.gz
sudo tar -xzf nerdctl-${NERDCTL_VERSION}-linux-${ARCH}.tar.gz -C /usr/local/bin
rm nerdctl-${NERDCTL_VERSION}-linux-${ARCH}.tar.gz

echo "=== Installing CNI plugins for networking ==="

# Install CNI plugins
CNI_VERSION="1.5.1"
sudo mkdir -p /opt/cni/bin
wget -q https://github.com/containernetworking/plugins/releases/download/v${CNI_VERSION}/cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz
sudo tar -xzf cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz -C /opt/cni/bin
rm cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz

# Configure CNI
sudo mkdir -p /etc/cni/net.d
cat <<'EOF' | sudo tee /etc/cni/net.d/10-containerd-net.conflist > /dev/null
{
  "cniVersion": "1.0.0",
  "name": "containerd-net",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "cni0",
      "isGateway": true,
      "ipMasq": true,
      "ipam": {
        "type": "host-local",
        "ranges": [
          [{"subnet": "10.88.0.0/16"}]
        ],
        "routes": [{"dst": "0.0.0.0/0"}]
      }
    },
    {
      "type": "portmap",
      "capabilities": {"portMappings": true}
    }
  ]
}
EOF

# Add kata-bridge network configuration (required for Kata + Cloud Hypervisor networking on ARM64)
cat <<'EOF' | sudo tee /etc/cni/net.d/10-kata-bridge.conflist > /dev/null
{
  "cniVersion": "1.0.0",
  "name": "kata-bridge",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "kata0",
      "isGateway": true,
      "ipMasq": true,
      "hairpinMode": true,
      "ipam": {
        "type": "host-local",
        "ranges": [[{ "subnet": "10.44.0.0/24", "gateway": "10.44.0.1" }]],
        "routes": [{ "dst": "0.0.0.0/0" }]
      }
    },
    { "type": "portmap", "capabilities": { "portMappings": true } },
    { "type": "firewall", "ingressPolicy": "same-bridge" },
    { "type": "tuning" }
  ]
}
EOF

echo "=== Setting up containerd permissions ==="

# Create containerd group and add ubuntu user
sudo groupadd -f containerd
sudo usermod -aG containerd ubuntu

# Configure containerd socket permissions
sudo mkdir -p /etc/systemd/system/containerd.service.d
cat <<'EOF' | sudo tee /etc/systemd/system/containerd.service.d/override.conf > /dev/null
[Service]
ExecStartPre=/bin/sh -c 'if [ -f /etc/containerd/config.toml.exedev ]; then cp -f /etc/containerd/config.toml.exedev /etc/containerd/config.toml; fi'
ExecStartPost=/bin/sh -c 'sleep 1 && chmod 660 /run/containerd/containerd.sock && chgrp containerd /run/containerd/containerd.sock'
EOF

# Add sudo permissions for container commands
cat <<'EOF' | sudo tee /etc/sudoers.d/99-containerd > /dev/null
# Allow ubuntu user to run container commands without password
ubuntu ALL=(ALL) NOPASSWD: /usr/local/bin/ctr
ubuntu ALL=(ALL) NOPASSWD: /usr/local/bin/nerdctl
EOF

echo "=== Starting services ==="

# Enable required kernel modules
sudo modprobe vhost_vsock
sudo modprobe vsock
sudo modprobe sch_ingress
sudo modprobe cls_u32
echo -e 'vhost_vsock\nvsock\nsch_ingress\ncls_u32' | sudo tee /etc/modules-load.d/kata.conf >/dev/null

# Create required directories
sudo install -d -m 0755 -o root -g root /run/kata-containers
sudo install -d -m 0755 -o root -g root /run/kata-containers/shared
sudo install -d -m 0755 -o root -g root /run/kata-containers/template

# Start services
sudo systemctl daemon-reload
sudo systemctl enable containerd
sudo systemctl start containerd
sleep 2

# Start nydus-snapshotter
sudo systemctl enable nydus-snapshotter
sudo systemctl start nydus-snapshotter
sleep 2

# Reload containerd to ensure proxy plugin is registered
sudo systemctl reload containerd
sleep 3

# Wait for nydus to register with containerd (proxy plugin can take a moment)
echo "Waiting for nydus to register with containerd..."
NYDUS_OK=0
for i in {1..20}; do
    if sudo ctr plugin ls | grep -q "io.containerd.snapshotter.*nydus.*ok"; then
        echo "  Nydus snapshotter registered successfully"
        NYDUS_OK=1
        break
    fi
    sleep 1
done
if [ "$NYDUS_OK" -ne 1 ]; then
    echo "ERROR: Nydus snapshotter not registered with containerd"
    exit 1
fi

# Fix socket permissions
if [ -S /run/containerd/containerd.sock ]; then
    sudo chmod 660 /run/containerd/containerd.sock
    sudo chgrp containerd /run/containerd/containerd.sock
fi

# Create the exe namespace
sudo ctr namespace create exe 2>/dev/null || true

echo "=== Configuring network isolation ==="

# Pre-create nerdctl bridge network with proper isolation
# Using a different subnet to avoid conflicts
sudo nerdctl -n exe network create bridge --subnet 10.5.0.0/16 2>/dev/null || true

# Configure iptables rules for network isolation
# Allow containers to reach internet but not each other or the host
sudo iptables -I FORWARD -i nerdctl0 -o nerdctl0 -j DROP  # Block container-to-container
sudo iptables -I INPUT -i nerdctl0 -j DROP  # Block container-to-host
sudo iptables -I INPUT -i nerdctl0 -p icmp -j ACCEPT  # Allow ICMP for network diagnostics
sudo iptables -I INPUT -i nerdctl0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT  # Allow established connections

# Block access to tailscale interface completely
sudo iptables -I FORWARD -i nerdctl0 -o tailscale0 -j DROP
sudo iptables -I FORWARD -i tailscale0 -o nerdctl0 -j DROP

# Ensure NAT is enabled for internet access
sudo iptables -t nat -A POSTROUTING -s 10.5.0.0/16 ! -o nerdctl0 -j MASQUERADE

# Save iptables rules to persist across reboots
sudo mkdir -p /etc/iptables
sudo iptables-save | sudo tee /etc/iptables/rules.v4 > /dev/null

# Install iptables-persistent to load rules on boot
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y iptables-persistent netfilter-persistent
sudo systemctl enable netfilter-persistent

echo "Network isolation configured"

echo "=== Configuring SSH MaxSessions ==="

# Set SSH MaxSessions to 50 for the machine
sudo sed -i '/^#*MaxSessions/d' /etc/ssh/sshd_config
echo "MaxSessions 50" | sudo tee -a /etc/ssh/sshd_config > /dev/null
sudo systemctl reload ssh
echo "SSH MaxSessions set to 50"

echo "=== Testing setup ==="

# Check services
echo "Checking service status..."
sudo systemctl is-active containerd >/dev/null 2>&1 && echo "✓ containerd is running" || echo "✗ containerd failed to start"
sudo systemctl is-active nydus-snapshotter >/dev/null 2>&1 && echo "✓ nydus-snapshotter is running" || echo "✗ nydus-snapshotter failed to start"

# Check nydus socket
if [ -S /run/containerd-nydus/containerd-nydus-grpc.sock ]; then
    echo "✓ Nydus socket exists"
else
    echo "✗ Nydus socket missing"
fi

# Test pulling with nydus
echo ""
echo "Testing nydus snapshotter..."
# Just verify the snapshotter is registered with containerd
if sudo ctr --namespace exe plugin ls | grep -q "io.containerd.snapshotter.*nydus.*ok"; then
    echo "✓ Nydus snapshotter registered with containerd"
else
    echo "✗ Nydus snapshotter not registered"
fi

# Pre-pull baseline images (digest-resolved) for exe namespace
echo ""
echo "Pre-pulling baseline images (exeuntu, ubuntu, alpine) by digest..."

normalize_arch() {
  local a="$(uname -m)"
  case "$a" in
    x86_64) echo amd64;;
    aarch64|arm64) echo arm64;;
    *) echo "$a";;
  esac
}

resolve_digest_ref() {
  # $1: canonical ref with tag (e.g., docker.io/library/ubuntu:latest)
  local ref="$1"
  local arch; arch=$(normalize_arch)
  # skopeo selects platform with --override-arch and returns that image's digest
  local digest
  if ! digest=$(skopeo inspect --override-os linux --override-arch "$arch" --format '{{.Digest}}' docker://"$ref" 2>/dev/null); then
    echo ""; return 1
  fi
  # Strip tag part and replace with @sha256
  local name_without_tag="${ref%:*}"
  echo "${name_without_tag}@${digest}"
}

pull_by_digest() {
  local ref="$1"
  local resolved
  if ! resolved=$(resolve_digest_ref "$ref"); then
    echo "  ! Failed to resolve digest for $ref"; return 1
  fi
  if [ -z "$resolved" ]; then
    echo "  ! Empty digest for $ref"; return 1
  fi
  echo "  pulling $resolved"
  # Use nydus snapshotter
  sudo nerdctl -n exe --snapshotter nydus pull "$resolved" >/dev/null 2>&1 || return 1
}

# Image refs to resolve
EXEUNTU_REF="ghcr.io/boldsoftware/exeuntu:latest"
UBUNTU_REF="docker.io/library/ubuntu:latest"
ALPINE_REF="docker.io/library/alpine:latest"

# Resolve alpine digest for use in test as well
ALPINE_RESOLVED="$(resolve_digest_ref "$ALPINE_REF" || true)"

pull_by_digest "$EXEUNTU_REF" || echo "  ! Could not pre-pull exeuntu"
pull_by_digest "$UBUNTU_REF" || echo "  ! Could not pre-pull ubuntu"
pull_by_digest "$ALPINE_REF" || echo "  ! Could not pre-pull alpine"

# Test running a container with Kata and verify Cloud Hypervisor is used
echo ""
echo "Testing Kata + Cloud Hypervisor..."
# Choose test image (prefer resolved alpine digest)
TEST_IMAGE="${ALPINE_RESOLVED:-docker.io/library/alpine:latest}"

# Start a test container in the background
TEST_CONTAINER="kata-clh-test-$$"
sudo ctr --namespace exe run --runtime io.containerd.kata.v2 -d "$TEST_IMAGE" $TEST_CONTAINER sleep 10 >/dev/null 2>&1 &
CTR_PID=$!

# Wait for container to start
sleep 3

# Check if Cloud Hypervisor process is running
if ps aux | grep -v grep | grep -q "/opt/kata/bin/cloud-hypervisor.*$TEST_CONTAINER"; then
    echo "✓ Kata + Cloud Hypervisor verified - Cloud Hypervisor process detected!"
    HYPERVISOR_OK=true
elif ps aux | grep -v grep | grep -q "qemu-system.*$TEST_CONTAINER"; then
    echo "✗ QEMU detected instead of Cloud Hypervisor!"
    HYPERVISOR_OK=false
else
    echo "✗ No hypervisor process detected for test container"
    HYPERVISOR_OK=false
fi

# Clean up test container
sudo ctr --namespace exe task kill $TEST_CONTAINER >/dev/null 2>&1 || true
sudo ctr --namespace exe container rm $TEST_CONTAINER >/dev/null 2>&1 || true
wait $CTR_PID 2>/dev/null || true

if [ "$HYPERVISOR_OK" = "false" ]; then
    echo "WARNING: Cloud Hypervisor not properly configured!"
fi

echo ""
echo "=== Setup complete ==="
echo ""
echo "System configured with:"
echo "  • Containerd ${CONTAINERD_VERSION}"
echo "  • Kata Containers ${KATA_VERSION} with Cloud Hypervisor"
echo "  • Nydus snapshotter ${NYDUS_VERSION} with nydusd ${NYDUSD_VERSION}"
echo "  • CNI networking"
echo "  • Data directory at /data/containerd"
echo "  • Namespace 'exe' created"
echo ""
echo "Commands available:"
echo "  sudo ctr -n exe <command>                                  # Use ctr with exe namespace"
echo "  sudo nerdctl -n exe --snapshotter nydus <command>         # Use nerdctl with nydus"
echo ""
echo "Example usage:"
echo "  sudo nerdctl -n exe --snapshotter nydus run --rm --runtime io.containerd.kata.v2 alpine:latest sh"
echo ""
echo "For Kata containers with proper networking on ARM64:"
echo "  sudo nerdctl run --net kata-bridge --runtime io.containerd.kata.v2 alpine:latest sh"
echo ""
echo "Note: You may need to log out and back in for group permissions to take effect."
