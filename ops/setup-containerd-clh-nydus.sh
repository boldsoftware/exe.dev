#!/bin/bash
set -euo pipefail
set -E # inherit traps
trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR

echo "=== Running setup-containerd-clh-nydus.sh ==="

if [ "${EXE_DEBUG_SETUP:-0}" = "1" ]; then
    set -x
fi

echo "=== Starting clean setup for $(hostname) with Cloud Hypervisor + Nydus ==="

# Prevent service restarts during package installation that could kill SSH/Tailscale
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# Detect if we're in a CI environment (no NVMe drives, ephemeral VM)
IS_CI_VM=0
if [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ] || [ ! -e /dev/nvme0n1 ]; then
    IS_CI_VM=1
    echo "=== CI/ephemeral VM detected, skipping swap and RAID setup ==="
fi

# Configure Huge Pages. cloud-hypervisor refuses to boot if huge pages are enabled in Kata but not
# actually reserved on the host. /proc/meminfo is reported in KB; default hugepages are 2MB, so
# divide by 4096 to reserve ~50% of RAM.
HUGEPAGE_TARGET=$(awk '/MemTotal/ { print int($2/4096); exit(0); }' /proc/meminfo)
echo "Setting vm.nr_hugepages=${HUGEPAGE_TARGET}"
echo "${HUGEPAGE_TARGET}" | sudo tee /proc/sys/vm/nr_hugepages >/dev/null
sudo mkdir -p /etc/sysctl.d
cat <<EOF | sudo tee /etc/sysctl.d/90-exe-hugepages.conf >/dev/null
# Ensure huge pages survive reboots; Kata's enable_hugepages requires them.
vm.nr_hugepages=${HUGEPAGE_TARGET}
EOF
sudo sysctl --system >/dev/null

# Swap and /local RAID setup is now handled by environment-specific scripts:
# - setup-host-part1.sh: Sets up swap and RAID 0 XFS mount for /local on metal instances
# - ci-vm-start.sh: Creates /local as a directory
# - setup-lima-hosts.sh: Creates /local as a directory

echo "=== Installing containerd ==="

# Install prerequisites
sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 apt-get install --no-install-recommends --no-upgrade -qq -y -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" \
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
    net-tools \
    skopeo >/dev/null 2>&1

# Install containerd from official releases (not apt) for specific version
CONTAINERD_VERSION="2.1.4"
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    ARCH="amd64"
elif [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
    ARCH="arm64"
fi

# Directory where artifacts are staged by the downloader (ubuntu user's cache)
ASSETS_DIR="/home/ubuntu/.cache/exedops"

# Download and install containerd
echo "Installing containerd ${CONTAINERD_VERSION} for ${ARCH}..."
cd /tmp
echo "Extracting containerd..."
sudo tar -xzf "${ASSETS_DIR}/containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz" -C /usr/local

# Install containerd systemd service
sudo mkdir -p /usr/local/lib/systemd/system
sudo cp "${ASSETS_DIR}/containerd.service" /usr/local/lib/systemd/system/containerd.service
sudo systemctl daemon-reload

# Install runc
RUNC_VERSION="1.1.14"
echo "Installing runc ${RUNC_VERSION}..."
sudo cp "${ASSETS_DIR}/runc-${RUNC_VERSION}.${ARCH}" /usr/local/sbin/runc
sudo chmod +x /usr/local/sbin/runc

echo "=== Installing Kata Containers with Cloud Hypervisor ==="

KATA_VERSION="3.20.0"
CLOUD_HYPERVISOR_VERSION="48.0"
# Kata uses the same arch naming as we normalized (amd64, arm64)
KATA_ARCH="$ARCH"

# Download and install Kata
echo "Installing Kata Containers ${KATA_VERSION}..."
cd /tmp
sudo tar -xf "${ASSETS_DIR}/kata-static-${KATA_VERSION}-${KATA_ARCH}.tar.xz" -C /

# Ensure Cloud Hypervisor is executable
sudo chmod +x /opt/kata/bin/cloud-hypervisor
sudo chmod +x /opt/kata/bin/containerd-shim-kata-v2

# Install cloud-hypervisor remote binary
echo "Installing cloud-hypervisor remote v${CLOUD_HYPERVISOR_VERSION}..."
sudo cp "${ASSETS_DIR}/ch-remote-static-${CLOUD_HYPERVISOR_VERSION}-${ARCH}" /opt/kata/bin/ch-remote
sudo chmod +x /opt/kata/bin/ch-remote

# Install custom kernel with nftables support (if available)
CUSTOM_KERNEL="${ASSETS_DIR}/vmlinux-6.12.42-nftables"
CUSTOM_CONFIG="${ASSETS_DIR}/config-6.12.42-nftables"
if [ -f "$CUSTOM_KERNEL" ]; then
    echo "Installing custom kernel with nftables support..."
    sudo cp "$CUSTOM_KERNEL" /opt/kata/share/kata-containers/vmlinux-6.12.42-nftables
    sudo chmod +x /opt/kata/share/kata-containers/vmlinux-6.12.42-nftables

    if [ -f "$CUSTOM_CONFIG" ]; then
        sudo cp "$CUSTOM_CONFIG" /opt/kata/share/kata-containers/config-6.12.42-nftables
    fi

    # Update the vmlinux.container symlink to point to our custom kernel
    sudo ln -sf vmlinux-6.12.42-nftables /opt/kata/share/kata-containers/vmlinux.container
    echo "Custom kernel installed and activated"
else
    echo "No custom kernel found at $CUSTOM_KERNEL, using default Kata kernel"
fi

# Link Kata binaries
sudo ln -sf /opt/kata/bin/kata-runtime /usr/local/bin/kata-runtime
sudo ln -sf /opt/kata/bin/containerd-shim-kata-v2 /usr/local/bin/containerd-shim-kata-v2
sudo ln -sf /opt/kata/bin/ch-remote /usr/local/bin/ch-remote

echo "=== Installing Nydus Snapshotter ==="

# Install nydus-snapshotter and nydusd daemon
NYDUS_VERSION="0.15.2"
NYDUSD_VERSION="2.2.5"
# Both nydus-snapshotter and nydusd use amd64 naming
NYDUS_ARCH="$ARCH"

# Download and install nydus-snapshotter
echo "Installing nydus-snapshotter v${NYDUS_VERSION}..."
cd /tmp
# Extract to temp dir first since binaries are in bin/ subdirectory
mkdir -p /tmp/nydus-extract
tar -xzf "${ASSETS_DIR}/nydus-snapshotter-v${NYDUS_VERSION}-linux-${NYDUS_ARCH}.tar.gz" -C /tmp/nydus-extract
sudo mv /tmp/nydus-extract/bin/* /usr/local/bin/
rm -rf /tmp/nydus-extract
sudo chmod +x /usr/local/bin/containerd-nydus-grpc

# Download and install nydusd daemon
echo "Installing nydusd daemon v${NYDUSD_VERSION}..."
cd /tmp
tar -xzf "${ASSETS_DIR}/nydus-static-v${NYDUSD_VERSION}-linux-${NYDUS_ARCH}.tgz"
sudo cp nydus-static/nydusd* /usr/local/bin/
sudo cp nydus-static/nydus-image /usr/local/bin/
sudo chmod +x /usr/local/bin/nydusd* /usr/local/bin/nydus-image
rm -rf nydus-static

# Create nydus configuration directory
sudo mkdir -p /etc/nydus
# Create proper nydusd configuration
NYDUS_CACHE_DIR="/local/nydus/cache"
cat <<EOF | sudo tee /etc/nydus/nydusd-config.json >/dev/null
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
        "work_dir": "$NYDUS_CACHE_DIR"
      }
    }
  },
  "mode": "direct",
  "digest_validate": false,
  "iostats_files": false
}
EOF

# Create nydus working directories
# /local should already exist (created by environment-specific setup)
if [ ! -d /local ]; then
    echo "ERROR: /local directory does not exist. It should be created by the environment setup script."
    exit 1
fi
sudo mkdir -p "/local/nydus/cache"
sudo mkdir -p /var/lib/containerd-nydus/snapshots
sudo mkdir -p /run/containerd-nydus

# Create systemd service for nydus-snapshotter
cat <<'EOF' | sudo tee /etc/systemd/system/nydus-snapshotter.service >/dev/null
[Unit]
Description=Nydus snapshotter for containerd
After=network-online.target
Wants=network-online.target
Before=containerd.service

[Service]
Type=simple
Environment="CONTAINERD_ADDRESS=/run/containerd/containerd.sock"
LimitNOFILE=1048576
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

# Copy our custom Cloud Hypervisor configuration
sudo mkdir -p /etc/kata-containers

# Kata config file must be placed in ${ASSETS_DIR} by the calling script
KATA_CONFIG="${ASSETS_DIR}/kata-config-clh.toml"
if [ ! -f "$KATA_CONFIG" ]; then
    echo "ERROR: kata-config-clh.toml not found in ${ASSETS_DIR}"
    echo "The calling script must copy kata-config-clh.toml to ${ASSETS_DIR} before running this script"
    exit 1
fi

echo "Installing Kata configuration from $KATA_CONFIG"
sudo cp "$KATA_CONFIG" /etc/kata-containers/configuration-clh.toml

# Create a symlink for default kata configuration to use CLH
sudo rm -f /etc/kata-containers/configuration.toml
sudo ln -s /etc/kata-containers/configuration-clh.toml /etc/kata-containers/configuration.toml

# Also update the default in /opt/kata to use CLH instead of QEMU
sudo rm -f /opt/kata/share/defaults/kata-containers/configuration.toml
sudo ln -s /etc/kata-containers/configuration-clh.toml /opt/kata/share/defaults/kata-containers/configuration.toml

echo "=== Configuring containerd with Nydus snapshotter ==="

# Determine containerd root directory
# For CI VMs and other ephemeral environments, use /var/lib
# For production metal instances, use /data (already mounted)
if [ $IS_CI_VM -eq 1 ]; then
    CONTAINERD_ROOT="/var/lib/containerd"
elif [ -d /data ] && mountpoint -q /data 2>/dev/null; then
    # Production: /data is a mounted XFS volume
    CONTAINERD_ROOT="/data/containerd"
    sudo mkdir -p $CONTAINERD_ROOT
else
    # Fallback: /data exists as a directory
    CONTAINERD_ROOT="/data/containerd"
    sudo mkdir -p $CONTAINERD_ROOT
fi

# Configure containerd with nydus as default snapshotter
sudo mkdir -p /etc/containerd
cat <<EOF | sudo tee /etc/containerd/config.toml >/dev/null
version = 2
root = "$CONTAINERD_ROOT"

[grpc]
  address = "/run/containerd/containerd.sock"

[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    sandbox_image = "registry.k8s.io/pause:3.9"

    # Registry configuration to use hosts.toml files
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"

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

    # CNI configuration (not used since we use nerdctl which manages its own networking)
    [plugins."io.containerd.grpc.v1.cri".cni]
      bin_dir = "/opt/cni/bin"
      conf_dir = "/etc/cni/net.d"

[proxy_plugins]
  [proxy_plugins.nydus]
    # Proxy snapshotter registration for nydus
    type = "snapshot"
    address = "/run/containerd-nydus/containerd-nydus-grpc.sock"

[plugins."io.containerd.internal.v1.opt"]
  path = "/opt/containerd"

[plugins."io.containerd.metadata.v1.bolt"]
  content_sharing_policy = "shared"
EOF
sudo cp /etc/containerd/config.toml /etc/containerd/config.toml.exedev

echo "=== Installing nerdctl ==="

# Configure containerd registry hosts for Docker Hub mirror
# This is used by both containerd and nerdctl
sudo mkdir -p /etc/containerd/certs.d/docker.io
cat <<'EOF' | sudo tee /etc/containerd/certs.d/docker.io/hosts.toml >/dev/null
server = "https://registry-1.docker.io"

[host."https://mirror.gcr.io"]
  capabilities = ["pull", "resolve"]
EOF

# Install nerdctl for easier container management (v2.1.3)
NERDCTL_VERSION="2.1.3"
# Map arch to nerdctl naming
NERDCTL_ARCH="$ARCH" # ARCH is normalized earlier to amd64/arm64
echo "Installing nerdctl ${NERDCTL_VERSION}..."
TMPD=$(mktemp -d)
if ! tar -xzf "${ASSETS_DIR}/nerdctl-${NERDCTL_VERSION}-linux-${NERDCTL_ARCH}.tar.gz" -C "$TMPD"; then
    echo "ERROR: failed to extract nerdctl archive" >&2
    rm -rf "$TMPD"
    exit 1
fi
# Find the nerdctl binary within the archive and install
NC_PATH=""
if [ -f "$TMPD/nerdctl" ]; then NC_PATH="$TMPD/nerdctl"; fi
if [ -z "$NC_PATH" ]; then
    NC_PATH=$(find "$TMPD" -maxdepth 2 -type f -name nerdctl | head -n1 || true)
fi
if [ -z "$NC_PATH" ]; then
    echo "ERROR: nerdctl binary not found in archive" >&2
    rm -rf "$TMPD"
    exit 1
fi
sudo install -m 0755 "$NC_PATH" /usr/local/bin/nerdctl
rm -rf "$TMPD"
echo "Installed: $(/usr/local/bin/nerdctl -v 2>&1 || echo failed)"

echo "=== Installing CNI plugins for networking ==="

# Install CNI plugins (required by nerdctl for its networking)
CNI_VERSION="1.5.1"
sudo mkdir -p /opt/cni/bin
echo "Installing CNI plugins ${CNI_VERSION}..."
cd /tmp
sudo tar -xzf "${ASSETS_DIR}/cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz" -C /opt/cni/bin

# Configure default CNI network for nerdctl with a larger subnet for thousands of containers
# This simplifies the network configuration and matches what works on AWS
sudo mkdir -p /etc/cni/net.d
cat <<'EOF' | sudo tee /etc/cni/net.d/nerdctl-bridge.conflist >/dev/null
{
  "cniVersion": "1.0.0",
  "name": "bridge",
  "nerdctlID": "17f29b073143d8cd97b5bbe492bdeffec1c5fee55cc1fe2112c8b9335f8b6121",
  "nerdctlLabels": {
    "nerdctl/default-network": "true"
  },
  "plugins": [
    {
      "type": "bridge",
      "bridge": "nerdctl0",
      "isGateway": true,
      "ipMasq": true,
      "hairpinMode": false,
      "ipam": {
        "ranges": [
          [
            {
              "gateway": "10.4.0.1",
              "subnet": "10.4.0.0/16"
            }
          ]
        ],
        "routes": [
          {
            "dst": "0.0.0.0/0"
          }
        ],
        "type": "host-local"
      }
    },
    {
      "type": "portmap",
      "capabilities": {
        "portMappings": true
      }
    }
  ]
}
EOF

# Note: All containers now use the default bridge network with port isolation for security

echo "=== Setting up containerd permissions ==="

# Create containerd group and add ubuntu user
sudo groupadd -f containerd
sudo usermod -aG containerd ubuntu

# Configure containerd socket permissions
sudo mkdir -p /etc/systemd/system/containerd.service.d
cat <<'EOF' | sudo tee /etc/systemd/system/containerd.service.d/override.conf >/dev/null
[Unit]
# containerd must start after the proxy snapshotter socket exists
Requires=nydus-snapshotter.service
After=nydus-snapshotter.service

[Service]
# Force containerd to use our config file
LimitNOFILE=1048576
ExecStart=
ExecStart=/usr/local/bin/containerd --config /etc/containerd/config.toml
ExecStartPre=/bin/sh -c 'if [ -f /etc/containerd/config.toml.exedev ]; then cp -f /etc/containerd/config.toml.exedev /etc/containerd/config.toml; fi'
ExecStartPost=/bin/sh -c 'sleep 1 && chmod 660 /run/containerd/containerd.sock && chgrp containerd /run/containerd/containerd.sock'
EOF

# Add sudo permissions for container commands
cat <<'EOF' | sudo tee /etc/sudoers.d/99-containerd >/dev/null
# Allow ubuntu user to run container commands without password
ubuntu ALL=(ALL) NOPASSWD: /usr/local/bin/ctr
ubuntu ALL=(ALL) NOPASSWD: /usr/local/bin/nerdctl
EOF

echo "=== Starting services ==="

# Enable required kernel modules for Kata
sudo modprobe vhost_vsock
sudo modprobe vsock
# TC modules for tcfilter networking model
sudo modprobe sch_ingress
sudo modprobe cls_u32
sudo modprobe cls_flower # Critical for tcfilter - must be loaded!
sudo modprobe act_mirred
sudo modprobe tap
echo -e 'vhost_vsock\nvsock\nsch_ingress\ncls_u32\ncls_flower\nact_mirred\ntap' | sudo tee /etc/modules-load.d/kata.conf >/dev/null

# Create required directories
sudo install -d -m 0755 -o root -g root /run/kata-containers
sudo install -d -m 0755 -o root -g root /run/kata-containers/shared
sudo install -d -m 0755 -o root -g root /run/kata-containers/template

# Start services
sudo systemctl daemon-reload
sudo systemctl enable nydus-snapshotter
sudo systemctl enable containerd

# Start nydus-snapshotter first (it must be running before containerd)
sudo systemctl start nydus-snapshotter
until systemctl is-active --quiet nydus-snapshotter; do
    sleep 0.1
done

# Now start containerd (which requires nydus-snapshotter)
sudo systemctl start containerd
until systemctl is-active --quiet containerd; do
    sleep 0.1
done

echo "Waiting for nydus to register with containerd..."
NYDUS_OK=0
for i in {1..120}; do
    # Check if nydus appears in plugin list with "ok" status
    if sudo ctr plugin ls 2>/dev/null | grep -E "nydus.*ok" >/dev/null 2>&1; then
        echo "  Nydus snapshotter registered successfully"
        NYDUS_OK=1
        break
    fi
    sleep 2
done
if [ "$NYDUS_OK" -ne 1 ]; then
    echo "ERROR: Nydus snapshotter not registered with containerd within 2min"
    echo "Current plugin status:"
    sudo ctr plugin ls || true
    exit 1
fi

# Fix socket permissions
if [ -S /run/containerd/containerd.sock ]; then
    sudo chmod 660 /run/containerd/containerd.sock
    sudo chgrp containerd /run/containerd/containerd.sock
fi

# Create the exe namespace
sudo ctr namespace create exe 2>/dev/null || true

echo "=== Configuring network settings and isolation ==="

# Enable IP forwarding (required for NAT)
sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1
echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf >/dev/null

# Set up network isolation using bridge port isolation
# This is more efficient than iptables and works better with Kata containers
CONTAINER_SUBNET="10.4.0.0/16"
BRIDGE_NAME="nerdctl0"

# Create a script to apply port isolation and minimal iptables rules
cat <<'ISOLATION_SCRIPT' | sudo tee /usr/local/bin/setup-container-isolation.sh >/dev/null
#!/bin/bash
set -e

CONTAINER_SUBNET="10.4.0.0/16"
BRIDGE_NAME="nerdctl0"
ALLOW_DEV_HOST_ACCESS="__ALLOW_DEV_HOST_ACCESS__"

# Wait for bridge to exist (it's created by CNI when first container starts)
if ip link show $BRIDGE_NAME >/dev/null 2>&1; then
    # Enable VLAN filtering on the bridge for port isolation
    if ! ip -d link show $BRIDGE_NAME 2>/dev/null | grep -q "vlan_filtering 1"; then
        ip link set $BRIDGE_NAME type bridge vlan_filtering 1
        echo "Enabled VLAN filtering on $BRIDGE_NAME"
    fi

    # Apply port isolation to all existing container interfaces
    for veth in $(bridge link show 2>/dev/null | grep "master $BRIDGE_NAME" | cut -d: -f2 | cut -d@ -f1); do
        bridge link set dev $veth isolated on flood off mcast_flood off bcast_flood off 2>/dev/null || true
        echo "Applied port isolation to $veth"
    done
fi

# Set up minimal iptables rules for host and network protection
# Function to add iptables rule if it doesn't exist (append semantics)
add_rule() {
    if ! iptables -C "$@" 2>/dev/null; then
        iptables -A "$@"
    fi
}

# Accept established/related traffic early on FORWARD to avoid breaking DNAT replies
add_rule FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

# Protect the host from container-initiated NEW connections (but allow established)
add_rule INPUT -s $CONTAINER_SUBNET -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
add_rule INPUT -s $CONTAINER_SUBNET -m conntrack --ctstate NEW -j DROP

# Development: Allow containers to access host gateway on port 8080 (e.g., for local services)
if [ -n "$ALLOW_DEV_HOST_ACCESS" ] && [ "$ALLOW_DEV_HOST_ACCESS" = "1" ]; then
    # Detect gateway IP (Mac host in Lima)
    GATEWAY_IP=$(getent ahostsv4 _gateway 2>/dev/null | awk '{print $1; exit}')
    if [ -n "$GATEWAY_IP" ]; then
        # Allow access to gateway:8080 before the general DROP rules
        add_rule FORWARD -s $CONTAINER_SUBNET -d $GATEWAY_IP -p tcp --dport 8080 -j ACCEPT
        echo "Development mode: Allowed container access to $GATEWAY_IP:8080"
    else
        echo "Warning: ALLOW_DEV_HOST_ACCESS set but could not determine gateway IP"
    fi
fi

# Block containers from accessing private networks and metadata services
# (Replies to host/Internet remain allowed by the ESTABLISHED rule above)
add_rule FORWARD -s $CONTAINER_SUBNET -d 192.168.0.0/16 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 172.16.0.0/12 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.0.0.0/14 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.5.0.0/16 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.6.0.0/15 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.8.0.0/13 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.16.0.0/12 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.32.0.0/11 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.64.0.0/10 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 10.128.0.0/9 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 169.254.169.254/32 -j DROP
add_rule FORWARD -s $CONTAINER_SUBNET -d 169.254.0.0/16 -j DROP

# Block Tailscale interfaces if they exist
for iface in tailscale0 utun0 utun1 utun2 utun3; do
    if ip link show $iface >/dev/null 2>&1; then
        add_rule FORWARD -s $CONTAINER_SUBNET -o $iface -j DROP
        add_rule FORWARD -i $iface -d $CONTAINER_SUBNET -j DROP
    fi
done

echo "Container isolation rules applied successfully"
ISOLATION_SCRIPT

# Substitute the ALLOW_DEV_HOST_ACCESS value into the script
sudo sed -i "s/__ALLOW_DEV_HOST_ACCESS__/${ALLOW_DEV_HOST_ACCESS:-}/g" /usr/local/bin/setup-container-isolation.sh

sudo chmod +x /usr/local/bin/setup-container-isolation.sh

# Apply the isolation rules now
sudo /usr/local/bin/setup-container-isolation.sh

# Note: Port isolation for individual containers is now handled by the container manager
# during container creation, so no monitoring service is needed

# Ensure rules persist across reboots
if ! grep -q setup-container-isolation /etc/rc.local 2>/dev/null; then
    if [ ! -f /etc/rc.local ]; then
        echo '#!/bin/sh -e' | sudo tee /etc/rc.local >/dev/null
    fi
    # Add before exit 0 if it exists, otherwise append
    if grep -q "^exit 0" /etc/rc.local; then
        sudo sed -i '/^exit 0/i /usr/local/bin/setup-container-isolation.sh' /etc/rc.local
    else
        echo '/usr/local/bin/setup-container-isolation.sh' | sudo tee -a /etc/rc.local >/dev/null
    fi
    sudo chmod +x /etc/rc.local
fi

echo "Network settings and isolation configured"

echo "=== Configuring SSH MaxSessions ==="

# Set SSH MaxSessions to 50 for the machine
sudo sed -i '/^#*MaxSessions/d' /etc/ssh/sshd_config
echo "MaxSessions 50" | sudo tee -a /etc/ssh/sshd_config >/dev/null
# Handle both regular SSH service and socket-activated SSH
if systemctl is-active ssh >/dev/null 2>&1; then
    sudo systemctl reload ssh
elif systemctl is-active ssh.socket >/dev/null 2>&1; then
    # Socket-activated SSH - configuration will be picked up on next connection
    echo "SSH is socket-activated, configuration will apply on next connection"
elif systemctl is-active sshd >/dev/null 2>&1; then
    sudo systemctl reload sshd
else
    echo "Warning: SSH service not found or not active, skipping reload"
fi
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

echo ""
echo "Loading baseline images (exeuntu, ubuntu, alpine)..."

# Images to load
IMAGES=(
    "ghcr.io/boldsoftware/exeuntu:latest"
    "ghcr.io/linuxcontainers/alpine:latest"
    "docker.io/library/ubuntu:latest"
)

for image in "${IMAGES[@]}"; do
    image_base=$(echo "$image" | sed 's|/|_|g' | sed 's|:|_|g')
    base_tar="${ASSETS_DIR}/${image_base}-${ARCH}.tar"

    if [ -f "$base_tar" ]; then
        echo "Loading $image from cache..."
        sudo ctr -n exe images import "$base_tar"
        # If the image is not visible to nerdctl after import, try nerdctl load, then fall back to pull
        if ! sudo nerdctl -n exe image inspect "$image" >/dev/null 2>&1; then
            echo "Image not visible to nerdctl after import; trying nerdctl load..."
            if sudo nerdctl -n exe load -i "$base_tar" >/dev/null 2>&1 &&
                sudo nerdctl -n exe image inspect "$image" >/dev/null 2>&1; then
                echo "✓ $image loaded via nerdctl (skip pull)"
            else
                echo "Image still not found; pulling $image to complete setup..."
                sudo nerdctl -n exe --snapshotter nydus pull "$image"
            fi
        else
            echo "✓ $image imported from cache (skip pull)"
        fi

        # Ensure the repo@digest alias exists so code that resolves tags to digests
        # can find the image without a network pull.
        # 1) Derive the manifest digest as seen by containerd for this ref.
        # Extract manifest digest for the imported tag without triggering SIGPIPE under pipefail.
        # Read full input and print the digest of the exact matching ref, or empty if not found.
        img_digest=$(sudo ctr -n exe images ls 2>/dev/null | awk -v img="$image" '($1==img){print $3; found=1} END{ if (!found) print "" }')
        if [ -n "$img_digest" ]; then
            repo_no_tag="${image%%:*}"
            digest_ref="${repo_no_tag}@${img_digest}"
            # 2) If the digest ref is not present, add a tag pointing to the same content.
            if ! sudo ctr -n exe images ls 2>/dev/null | awk -v ref="$digest_ref" '($1==ref){found=1} END{exit !found}'; then
                echo "Tagging $image as $digest_ref (repo@digest alias)"
                sudo ctr -n exe images tag "$image" "$digest_ref" || true
            fi
        else
            echo "Warning: failed to determine manifest digest for $image; skipping alias tag"
        fi
    else
        echo "Pulling $image from registry..."
        sudo nerdctl -n exe --snapshotter nydus pull "$image"
    fi
done

echo ""
echo "Testing kata runtime with nydus snapshotter..."

set -x
sudo nerdctl -n exe --snapshotter nydus run --rm --runtime io.containerd.kata.v2 alpine true
set +x

echo ""
echo "=== Setup complete ==="
echo "Versions: containerd ${CONTAINERD_VERSION} (root ${CONTAINERD_ROOT}), Kata ${KATA_VERSION}, Nydus ${NYDUS_VERSION}"
echo ""
