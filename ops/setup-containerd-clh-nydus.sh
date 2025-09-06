#!/bin/bash
set -euo pipefail

echo "=== Running setup-containerd-clh-nydus.sh ==="

# Optional verbose tracing: export EXE_DEBUG_SETUP=1
if [ "${EXE_DEBUG_SETUP:-0}" = "1" ]; then
	set -x
fi

# On any error, emit useful diagnostics before exiting
trap 'rc=$?; echo "ERROR: setup failed at line $LINENO: $BASH_COMMAND (exit $rc)" >&2; \
  echo "--- Service statuses ---"; \
  systemctl -q is-active containerd  && systemctl --no-pager -l status containerd  || true; \
  systemctl -q is-active nydus-snapshotter && systemctl --no-pager -l status nydus-snapshotter || true; \
  systemctl -q is-active exe-clh-snapshot && systemctl --no-pager -l status exe-clh-snapshot || true; \
  systemctl -q is-active exe-kata-pool && systemctl --no-pager -l status exe-kata-pool || true; \
  echo "--- Recent logs ---"; \
  journalctl -n 120 --no-pager -u containerd -u nydus-snapshotter -u exe-clh-snapshot -u exe-kata-pool || true; \
  exit $rc' ERR

echo "=== Starting clean setup for $(hostname) with Cloud Hypervisor + Nydus ==="

# Prevent service restarts during package installation that could kill SSH/Tailscale
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# Detect if we're in a CI environment (no NVMe drives, ephemeral VM)
IS_CI_VM=0
if [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ] || [ ! -e /dev/nvme0n1 ]; then
	IS_CI_VM=1
	echo "=== CI/ephemeral VM detected, skipping swap and data volume setup ==="
fi

if [ $IS_CI_VM -eq 0 ]; then
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
fi

echo "=== Installing containerd ==="

# Install prerequisites
sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 apt-get install -qq -y -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" \
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
	skopeo >/dev/null 2>&1

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
# Kata uses the same arch naming as we normalized (amd64, arm64)
KATA_ARCH="$ARCH"

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
# Both nydus-snapshotter and nydusd use amd64 naming
NYDUS_ARCH="$ARCH"

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
cat <<'EOF' | sudo tee /etc/nydus/nydusd-config.json >/dev/null
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
cat <<'EOF' | sudo tee /etc/systemd/system/nydus-snapshotter.service >/dev/null
[Unit]
Description=Nydus snapshotter for containerd
After=network-online.target
Wants=network-online.target
Before=containerd.service

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

echo "=== Skipping Kata VM templating; using CLH snapshot/restore path ==="

# Allow OCI annotations required for CLH restore (permit all to avoid drift)
KATA_CFG="/etc/kata-containers/configuration-clh.toml"
if sudo grep -q '^\[runtime\]' "$KATA_CFG"; then
	# Replace or insert enable_annotations under [runtime]; allow all via regex ".*"
	if sudo grep -q '^enable_annotations' "$KATA_CFG"; then
		sudo sed -i 's/^enable_annotations.*/enable_annotations = [".*"]/g' "$KATA_CFG"
	else
		sudo sed -i '/^\[runtime\]/a enable_annotations = [".*"]' "$KATA_CFG"
	fi
else
	sudo bash -c "cat >> '$KATA_CFG' <<'EOF'
[runtime]
enable_annotations = [".*"]
EOF"
fi
echo "Enabled Kata OCI annotations for CLH restore in $KATA_CFG (enable_annotations = [\".*\"])"

# Tune Cloud Hypervisor memory defaults: small base (256MiB) + large headroom, enable balloon reclaim
echo "Tuning Cloud Hypervisor memory defaults (base=256MiB, max=8192MiB, balloon reclaim on)"
# Update default_memory and default_maxmemory if present; otherwise append under [hypervisor.clh]
if sudo grep -q '^default_memory' "$KATA_CFG"; then
	sudo sed -i 's/^default_memory[[:space:]]*=.*/default_memory = 256/' "$KATA_CFG"
else
	sudo sed -i '/^\[hypervisor\.clh\]/a default_memory = 256' "$KATA_CFG"
fi
if sudo grep -q '^default_maxmemory' "$KATA_CFG"; then
	sudo sed -i 's/^default_maxmemory[[:space:]]*=.*/default_maxmemory = 8192/' "$KATA_CFG"
else
	sudo sed -i '/^\[hypervisor\.clh\]/a default_maxmemory = 8192' "$KATA_CFG"
fi
# Enable guest free memory reclaim via balloon
if sudo grep -q '^reclaim_guest_freed_memory' "$KATA_CFG"; then
	sudo sed -i 's/^#\?reclaim_guest_freed_memory[[:space:]]*=.*/reclaim_guest_freed_memory = true/' "$KATA_CFG"
else
	sudo sed -i '/^\[hypervisor\.clh\]/a reclaim_guest_freed_memory = true' "$KATA_CFG"
fi
echo "--- Kata hypervisor.clh memory config ---"
sudo awk '/^\[hypervisor\.clh\]/{f=1;print;next}/^\[/{f=0}f{print}' "$KATA_CFG" | sed -n '1,80p'
echo "----------------------------------------"

# Show effective runtime section for visibility
echo "--- Kata runtime config ---"
sudo awk '/^\[runtime\]/{f=1;print;next}/^\[/{f=0}f{print}' "$KATA_CFG"
echo "---------------------------"

# Ensure guest memory hotplug onlines memory by default
if sudo grep -q '^kernel_params' "$KATA_CFG"; then
	if ! sudo grep -q 'memhp_default_state=online' "$KATA_CFG"; then
		sudo sed -i 's/^kernel_params[[:space:]]*=[[:space:]]*"\(.*\)"/kernel_params = "\1 memhp_default_state=online"/' "$KATA_CFG"
	fi
else
	sudo sed -i '/^\[hypervisor\.clh\]/a kernel_params = "memhp_default_state=online"' "$KATA_CFG"
fi

# Ensure Cloud Hypervisor API socket is available for snapshots (use Kata+CLH default)
CLH_CFG="/etc/kata-containers/configuration-clh.toml"
if sudo grep -q '^\[hypervisor\.clh\]' "$CLH_CFG"; then
	# Prefer not to override; but if api_socket exists, set to default per-VM path used by Kata+CLH
	if sudo grep -q '^api_socket' "$CLH_CFG"; then
		sudo sed -i 's#^api_socket.*#api_socket = "/run/vc/vm/%s/clh-api.sock"#g' "$CLH_CFG"
	fi
else
	# No hypervisor.clh section; leave defaults
	true
fi

# Show the effective hypervisor.clh section for CLH
echo "--- Kata hypervisor.clh config ---"
sudo awk '/^\[hypervisor\.clh\]/{f=1;print;next}/^\[/{f=0}f{print}' "$CLH_CFG" || true
echo "---------------------------------"

echo "=== Configuring containerd with Nydus snapshotter ==="

# Create data directory (use /var/lib for CI VMs without /data volume)
if [ $IS_CI_VM -eq 1 ]; then
	CONTAINERD_ROOT="/var/lib/containerd"
else
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
    # Proxy snapshotter registration for nydus
    type = "snapshot"
    address = "/run/containerd-nydus/containerd-nydus-grpc.sock"

[plugins."io.containerd.internal.v1.opt"]
  path = "/opt/containerd"

[plugins."io.containerd.metadata.v1.bolt"]
  content_sharing_policy = "shared"
EOF
sudo cp /etc/containerd/config.toml /etc/containerd/config.toml.exedev

# Prepare Cloud Hypervisor snapshot/restore support and a ready pool
echo "=== Configuring Cloud Hypervisor snapshot/restore and warm pool ==="

# Install ch-remote if available for this CLH build; fallback is to skip snapshot creation
install_ch_remote() {
	if command -v ch-remote >/dev/null 2>&1; then
		return 0
	fi
	# Use a local variable so we don't clobber global ARCH (normalized to amd64/arm64)
	local CLH_ARCH_RAW
	CLH_ARCH_RAW=$(uname -m)
	local SUFFIX=""
	case "$CLH_ARCH_RAW" in
	x86_64) SUFFIX="" ;;
	aarch64 | arm64) SUFFIX="-aarch64" ;;
	*) SUFFIX="" ;;
	esac
	# Allow manual override: set EXE_CH_REMOTE_RELEASE to a tag like v47.0
	if [ -n "${EXE_CH_REMOTE_RELEASE:-}" ]; then
		CANDIDATES="$EXE_CH_REMOTE_RELEASE"
	else
		# Extract version (e.g., 47.0 or 47.0.0) from cloud-hypervisor --version
		VER=$(/opt/kata/bin/cloud-hypervisor --version 2>/dev/null | grep -oE '[0-9]+(\.[0-9]+){1,2}' | head -n1)
		if [ -z "$VER" ]; then
			echo "Warning: cannot detect Cloud Hypervisor version; skipping ch-remote install" >&2
			return 0
		fi
		CANDIDATES="v${VER}"
		# Add fallback tags: if has patch .0, try vX.Y; if has only X.Y, try vX.Y.0
		if echo "$VER" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
			PATCH=$(echo "$VER" | awk -F. '{print $3}')
			if [ "$PATCH" = "0" ]; then
				CANDIDATES="$CANDIDATES v$(echo "$VER" | awk -F. '{print $1"."$2}')"
			fi
		elif echo "$VER" | grep -qE '^[0-9]+\.[0-9]+$'; then
			CANDIDATES="$CANDIDATES v${VER}.0"
		fi
	fi
	TMP=$(mktemp)
	for REL in $CANDIDATES; do
		URL="https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/${REL}/ch-remote-static${SUFFIX}"
		echo "Attempting to download ch-remote from: $URL"
		if curl -fSL "$URL" -o "$TMP"; then
			sudo mv "$TMP" /usr/local/bin/ch-remote
			sudo chmod +x /usr/local/bin/ch-remote
			return 0
		else
			echo "Download failed: $URL" >&2
		fi
	done
	rm -f "$TMP"
	echo "Warning: failed to download ch-remote (tried: $CANDIDATES, suffix $SUFFIX)" >&2
}
install_ch_remote || true

# Snapshot builder: create a base CLH snapshot from a minimal Kata sandbox
SNAP_DIR=/var/lib/cloud-hypervisor/snapshots
sudo mkdir -p "$SNAP_DIR"
cat <<'EOF' | sudo tee /usr/local/sbin/exe-clh-snapshot >/dev/null
#!/bin/sh
set -eu
SNAP_DIR=${SNAP_DIR:-/var/lib/cloud-hypervisor/snapshots}
IMG="docker.io/library/alpine:latest"
NS="exe"

# Ensure nerdctl exists
command -v nerdctl >/dev/null 2>&1 || exit 0

# Ensure namespace exists (for nerdctl)
sudo nerdctl -n "$NS" info >/dev/null 2>&1 || true

name="kata-snap-$$-$(date +%s)"
sudo nerdctl -n "$NS" --snapshotter nydus run -d --runtime io.containerd.kata.v2 --name "$name" "$IMG" sleep 120 >/dev/null 2>&1 || exit 1

# Determine container ID for this name
CID="$(sudo nerdctl -n "$NS" inspect -f '{{.ID}}' "$name" 2>/dev/null || true)"
if [ -z "$CID" ]; then
  CID="$(sudo nerdctl -n "$NS" ps -a --format '{{.ID}}\t{{.Names}}' | awk -v n="$name" '$2==n{print $1; exit}')"
fi

sock=""
# Prefer well-known Kata+CLH defaults first (ID-specific)
for s in \
  "/run/vc/vm/${CID}/clh-api.sock" \
  "/run/kata-containers/${CID}/clh-api.sock" \
  "/run/vc/vm/${CID}/cloud-hypervisor.sock" \
  "/run/kata-containers/${CID}/cloud-hypervisor.sock"; do
  [ -S "$s" ] && sock="$s" && break
done
# Fallback: parse any running CLH for --api-socket (don't rely on container name)
if [ -z "$sock" ]; then
  clh_line=$(ps aux | grep -v grep | grep "/opt/kata/bin/cloud-hypervisor" | grep -- "--api-socket" || true)
  [ -n "$clh_line" ] && sock=$(printf "%s" "$clh_line" | sed -n 's/.*--api-socket \([^ ]\+\).*/\1/p' | head -n1)
fi
tries=0
while [ -z "$sock" ] && [ $tries -lt 100 ]; do
  # Prefer socket under ID-specific directories
  for s in \
    "/run/vc/vm/${CID}/clh-api.sock" \
    "/run/kata-containers/${CID}/clh-api.sock" \
    "/run/vc/vm/${CID}/cloud-hypervisor.sock" \
    "/run/kata-containers/${CID}/cloud-hypervisor.sock" \
    /run/vc/vm/*/clh-api.sock \
    /run/kata-containers/*/clh-api.sock \
    /run/vc/vm/*/cloud-hypervisor.sock \
    /run/kata-containers/*/cloud-hypervisor.sock; do
    [ -S "$s" ] || continue
    sock="$s"; break
  done
  tries=$((tries+1))
  [ -n "$sock" ] || sleep 0.1
done
if [ -z "$sock" ]; then
  echo "No Cloud Hypervisor API socket found for sandbox $name after waiting" >&2
  exit 1
fi

mkdir -p "$SNAP_DIR"
rm -f "$SNAP_DIR/config.json" "$SNAP_DIR/state.json" "$SNAP_DIR/memory-ranges" 2>/dev/null || true

if command -v ch-remote >/dev/null 2>&1; then
  # Pause VM before snapshot for consistency
  ch-remote --api-socket "$sock" pause >/dev/null 2>&1 || true
  # Create a snapshot to the destination directory (CLH expects a file:// URL)
  ch-remote --api-socket "$sock" snapshot "file://$SNAP_DIR"
  # Resume VM (best-effort)
  ch-remote --api-socket "$sock" resume >/dev/null 2>&1 || true
  # Provide compatibility symlinks for tools expecting base.snapshot/base.mem
  if [ -f "$SNAP_DIR/state.json" ] && [ -f "$SNAP_DIR/memory-ranges" ]; then
    ln -sf "$SNAP_DIR/state.json" "$SNAP_DIR/base.snapshot" 2>/dev/null || true
    ln -sf "$SNAP_DIR/memory-ranges" "$SNAP_DIR/base.mem" 2>/dev/null || true
  fi
else
  echo "ch-remote not found; skipping snapshot creation" >&2
fi

# Cleanup the builder container
sudo nerdctl -n "$NS" kill "$name" >/dev/null 2>&1 || true
sudo nerdctl -n "$NS" rm -f "$name" >/dev/null 2>&1 || true
EOF
sudo chmod +x /usr/local/sbin/exe-clh-snapshot

# Ready pool: maintain a small number of warm Kata sandboxes (sleeping); labeled for visibility
POOL_SIZE=3
cat <<'EOF' | sudo tee /usr/local/sbin/exe-kata-pool >/dev/null
#!/bin/sh
set -eu
NS="exe"
IMG="docker.io/library/alpine:latest"
POOL_SIZE=${POOL_SIZE:-3}

command -v ctr >/dev/null 2>&1 || exit 0

# Ensure namespace exists
sudo ctr namespace create "$NS" >/dev/null 2>&1 || true

nydus_ok() {
  sudo ctr plugin ls | grep -Eq "io.containerd.snapshotter.v1[[:space:]]+nydus[[:space:]].*[[:space:]]+ok"
}

running() {
  sudo ctr --namespace "$NS" c ls | awk '{print $1}' | grep -E '^exe-pool-' | wc -l | tr -d ' '
}

mkone() {
  # Generate a unique name without relying on $RANDOM (dash)
  suf=$(date +%s%N | tail -c 8)
  n="exe-pool-${suf}"
  sudo nerdctl -n "$NS" --snapshotter nydus run -d --runtime io.containerd.kata.v2 --name "$n" "$IMG" sleep 3600 >/dev/null 2>&1 || true
}

if ! nydus_ok; then
  # Nydus not ready; let timer retry later without blocking
  exit 0
fi

cur=$(running)
attempts=0
max_attempts=$((POOL_SIZE * 5))
while [ "$cur" -lt "$POOL_SIZE" ] && [ "$attempts" -lt "$max_attempts" ]; do
  mkone || true
  sleep 1
  cur=$(running)
  attempts=$((attempts+1))
done

exit 0
EOF
sudo chmod +x /usr/local/sbin/exe-kata-pool

cat <<'EOF' | sudo tee /etc/systemd/system/exe-clh-snapshot.service >/dev/null
[Unit]
Description=Build Cloud Hypervisor base snapshot
After=containerd.service nydus-snapshotter.service
Wants=containerd.service nydus-snapshotter.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/exe-clh-snapshot
TimeoutSec=180

[Install]
WantedBy=multi-user.target
EOF

cat <<'EOF' | sudo tee /etc/systemd/system/exe-kata-pool.service >/dev/null
[Unit]
Description=Maintain a pool of warm Kata sandboxes
After=containerd.service nydus-snapshotter.service exe-clh-snapshot.service
Wants=containerd.service nydus-snapshotter.service exe-clh-snapshot.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/exe-kata-pool
RemainAfterExit=yes
ExecStartPost=/bin/sh -c 'systemctl start exe-kata-pool.timer'

[Install]
WantedBy=multi-user.target
EOF

cat <<'EOF' | sudo tee /etc/systemd/system/exe-kata-pool.timer >/dev/null
[Unit]
Description=Refresh Kata warm pool periodically

[Timer]
OnBootSec=1min
OnUnitActiveSec=2min
AccuracySec=10s

[Install]
WantedBy=timers.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable exe-clh-snapshot.service >/dev/null 2>&1 || true
sudo systemctl enable exe-kata-pool.service >/dev/null 2>&1 || true
sudo systemctl enable exe-kata-pool.timer >/dev/null 2>&1 || true

echo "=== Installing nerdctl ==="

# Install nerdctl for easier container management (v2.1.3)
NERDCTL_VERSION="2.1.3"
# Map arch to nerdctl naming
NERDCTL_ARCH="$ARCH" # ARCH is normalized earlier to amd64/arm64
NERDCTL_URL="https://github.com/containerd/nerdctl/releases/download/v${NERDCTL_VERSION}/nerdctl-${NERDCTL_VERSION}-linux-${NERDCTL_ARCH}.tar.gz"
echo "Downloading nerdctl from: $NERDCTL_URL"
TMPD=$(mktemp -d)
if ! curl -fSL "$NERDCTL_URL" -o "$TMPD/nerdctl.tgz"; then
	echo "ERROR: failed to download nerdctl from $NERDCTL_URL" >&2
	rm -rf "$TMPD"
	exit 1
fi
if ! tar -xzf "$TMPD/nerdctl.tgz" -C "$TMPD"; then
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

# Install CNI plugins
CNI_VERSION="1.5.1"
sudo mkdir -p /opt/cni/bin
wget -q https://github.com/containernetworking/plugins/releases/download/v${CNI_VERSION}/cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz
sudo tar -xzf cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz -C /opt/cni/bin
rm cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz

# Configure CNI
sudo mkdir -p /etc/cni/net.d
cat <<'EOF' | sudo tee /etc/cni/net.d/10-containerd-net.conflist >/dev/null
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
cat <<'EOF' | sudo tee /etc/cni/net.d/10-kata-bridge.conflist >/dev/null
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
cat <<'EOF' | sudo tee /etc/systemd/system/containerd.service.d/override.conf >/dev/null
[Unit]
# containerd must start after the proxy snapshotter socket exists
Requires=nydus-snapshotter.service
After=nydus-snapshotter.service

[Service]
# Force containerd to use our config file
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
sudo systemctl enable nydus-snapshotter
sudo systemctl enable containerd

# Start nydus-snapshotter first (it must be running before containerd)
sudo systemctl start nydus-snapshotter
sleep 2

# Now start containerd (which requires nydus-snapshotter)
sudo systemctl start containerd
sleep 3

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
sudo iptables -I FORWARD -i nerdctl0 -o nerdctl0 -j DROP                                # Block container-to-container
sudo iptables -I INPUT -i nerdctl0 -j DROP                                              # Block container-to-host
sudo iptables -I INPUT -i nerdctl0 -p icmp -j ACCEPT                                    # Allow ICMP for network diagnostics
sudo iptables -I INPUT -i nerdctl0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT # Allow established connections

# Block access to tailscale interface completely
sudo iptables -I FORWARD -i nerdctl0 -o tailscale0 -j DROP
sudo iptables -I FORWARD -i tailscale0 -o nerdctl0 -j DROP

# Ensure NAT is enabled for internet access
sudo iptables -t nat -A POSTROUTING -s 10.5.0.0/16 ! -o nerdctl0 -j MASQUERADE

# Save iptables rules to persist across reboots
sudo mkdir -p /etc/iptables
sudo iptables-save | sudo tee /etc/iptables/rules.v4 >/dev/null

# Install iptables-persistent to load rules on boot
sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y iptables-persistent netfilter-persistent >/dev/null 2>&1
sudo systemctl enable netfilter-persistent

echo "Network isolation configured"

echo "=== Configuring SSH MaxSessions ==="

# Set SSH MaxSessions to 50 for the machine
sudo sed -i '/^#*MaxSessions/d' /etc/ssh/sshd_config
echo "MaxSessions 50" | sudo tee -a /etc/ssh/sshd_config >/dev/null
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

# Test nydus snapshotter registration
echo ""
echo "Testing nydus snapshotter..."
# Verify the snapshotter is registered with containerd (no namespace), with a short retry
nydus_ok_msg() {
	sudo ctr plugin ls | grep -q "io.containerd.snapshotter.*nydus.*ok"
}
NYDUS_REGISTERED=0
if nydus_ok_msg; then
	NYDUS_REGISTERED=1
else
	for i in 1 2 3 4 5; do
		sleep 1
		if nydus_ok_msg; then
			NYDUS_REGISTERED=1
			break
		fi
	done
fi
if [ "$NYDUS_REGISTERED" -eq 1 ]; then
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
	x86_64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) echo "$a" ;;
	esac
}

resolve_digest_ref() {
	# $1: canonical ref with tag (e.g., docker.io/library/ubuntu:latest)
	local ref="$1"
	local arch
	arch=$(normalize_arch)
	# skopeo selects platform with --override-arch and returns that image's digest
	local digest
	if ! digest=$(skopeo inspect --override-os linux --override-arch "$arch" --format '{{.Digest}}' docker://"$ref" 2>/dev/null); then
		echo ""
		return 1
	fi
	# Strip tag part and replace with @sha256
	local name_without_tag="${ref%:*}"
	echo "${name_without_tag}@${digest}"
}

pull_by_digest() {
	local ref="$1"
	local resolved
	if ! resolved=$(resolve_digest_ref "$ref"); then
		echo "  ! Failed to resolve digest for $ref"
		return 1
	fi
	if [ -z "$resolved" ]; then
		echo "  ! Empty digest for $ref"
		return 1
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

# Pull the image for ctr (ctr does NOT auto-pull)
sudo ctr -n exe images pull "$TEST_IMAGE" >/dev/null 2>&1 || true

# Start a test container in the background
TEST_CONTAINER="kata-clh-test-$$"
sudo ctr --namespace exe run --runtime io.containerd.kata.v2 -d "$TEST_IMAGE" $TEST_CONTAINER sleep 10 >/dev/null 2>&1 &
CTR_PID=$!

# Wait for container to start
sleep 3

# Check if Cloud Hypervisor process is running (do not rely on container name)
if pgrep -f "/opt/kata/bin/cloud-hypervisor" >/dev/null 2>&1; then
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

# Quick validation: run a kata container with CLH restore annotations (if snapshot exists)
if [ -f /var/lib/cloud-hypervisor/snapshots/state.json ] && [ -f /var/lib/cloud-hypervisor/snapshots/config.json ]; then
	echo "✓ Cloud Hypervisor snapshot directory present (state.json, config.json)"
fi

# Start snapshot creation and warm pool now that images are present
if ! sudo systemctl start exe-clh-snapshot.service; then
	echo "✗ exe-clh-snapshot.service failed; last logs:" >&2
	sudo systemctl -l --no-pager status exe-clh-snapshot.service || true
	sudo journalctl -n 120 --no-pager -u exe-clh-snapshot.service || true
else
	echo "✓ exe-clh-snapshot.service started"
fi
# Start pool population via timer (do not block on oneshot service)
sudo systemctl start exe-kata-pool.timer || true
echo "✓ exe-kata-pool.timer started (pool will populate in background)"

# Measure CLH restore timing using the unified restore annotation, if snapshot files exist
if [ -f /var/lib/cloud-hypervisor/snapshots/state.json ] && [ -f /var/lib/cloud-hypervisor/snapshots/memory-ranges ]; then
	echo ""
	echo "Testing Cloud Hypervisor restore timing (nerdctl + Kata)..."
	TEST_IMG="${ALPINE_RESOLVED:-docker.io/library/alpine:latest}"
	start_ms=$(date +%s%3N)
	if sudo nerdctl -n exe --snapshotter nydus run --rm --runtime io.containerd.kata.v2 \
		--annotation io.katacontainers.config.hypervisor.restore=source_url=file:///var/lib/cloud-hypervisor/snapshots \
		"$TEST_IMG" true >/dev/null 2>&1; then
		end_ms=$(date +%s%3N)
		echo "✓ CLH restore test succeeded in $((end_ms - start_ms)) ms"
	else
		end_ms=$(date +%s%3N)
		echo "✗ CLH restore test failed in $((end_ms - start_ms)) ms"
	fi
fi

echo ""
echo "=== Setup complete ==="
echo ""
echo "System configured with:"
echo "  • Containerd ${CONTAINERD_VERSION}"
echo "  • Kata Containers ${KATA_VERSION} with Cloud Hypervisor"
echo "  • Nydus snapshotter ${NYDUS_VERSION} with nydusd ${NYDUSD_VERSION}"
echo "  • CNI networking"
if [ $IS_CI_VM -eq 1 ]; then
	echo "  • Data directory at /var/lib/containerd"
else
	echo "  • Data directory at /data/containerd"
fi
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
echo "  sudo nerdctl -n exe run --net kata-bridge --runtime io.containerd.kata.v2 alpine:latest sh"
echo ""
echo "Note: You may need to log out and back in for group permissions to take effect."
