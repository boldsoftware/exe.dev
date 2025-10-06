#!/bin/bash
set -euo pipefail
set -E # inherit traps
trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR

# Configuration
LIMA_BASE="exe-ctr-base"
LIMA_HOST_A="exe-ctr"
LIMA_HOST_B="exe-ctr-tests"
CPUS=8
MEMORY=8
DISK=100

# Determine repo ops dir
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_SCRIPT_PATH="${OPS_DIR}/setup-containerd-clh-nydus.sh"
if [[ ! -f "$SETUP_SCRIPT_PATH" ]]; then
	echo "Required setup script not found: $SETUP_SCRIPT_PATH" >&2
	exit 1
fi

LIMA_DIR="$HOME/.lima"

# Provision a fresh Lima VM with containerd + Kata + Nydus
provision_base_vm() {
	local script_dir="${OPS_DIR}"
	if [ ! -f "${script_dir}/setup-containerd-clh-nydus.sh" ]; then
		echo "Error: setup-containerd-clh-nydus.sh not found in ${script_dir}"
		return 1
	fi

	# Create ubuntu user for compatibility
	echo "Creating ubuntu user for compatibility with production..."
	limactl shell ${LIMA_BASE} -- sudo useradd -m -s /bin/bash ubuntu 2>/dev/null || true
	echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' | limactl shell ${LIMA_BASE} -- sudo tee /etc/sudoers.d/ubuntu >/dev/null

	# Set up data and local directories
	echo "Setting up /data and /local directories for Lima..."
	limactl shell ${LIMA_BASE} -- sudo mkdir -p /data
	limactl shell ${LIMA_BASE} -- sudo chmod 755 /data
	limactl shell ${LIMA_BASE} -- sudo mkdir -p /local
	limactl shell ${LIMA_BASE} -- sudo chmod 755 /local

	# Download dependencies locally if not cached
	VM_ARCH="arm64"
	echo "Ensuring dependencies are downloaded for $VM_ARCH..."
	"${script_dir}/download-ctr-host.sh" "$VM_ARCH"

	echo "Copying setup script and config files to VM..."
	# Copy to /tmp first to avoid read-only filesystem issues
	limactl shell ${LIMA_BASE} -- cp "${script_dir}/setup-containerd-clh-nydus.sh" /tmp/setup-containerd-clh-nydus.sh
	limactl shell ${LIMA_BASE} -- cp "${script_dir}/kata-config-clh.toml" /tmp/kata-config-clh.toml

	# Copy pre-downloaded tarballs to VM
	echo "Copying pre-downloaded dependencies to VM..."
	CACHE_DIR="$HOME/.cache/exedops"
	for file in "$CACHE_DIR"/*.tar.gz "$CACHE_DIR"/*.tar.xz "$CACHE_DIR"/*.tgz "$CACHE_DIR"/*.service "$CACHE_DIR"/runc-* "$CACHE_DIR"/ch-remote-static-* "$CACHE_DIR"/*.tar; do
		if [ -f "$file" ]; then
			basename=$(basename "$file")
			echo "  Copying $basename..."
			limactl shell ${LIMA_BASE} -- cp "$file" /tmp/$basename
		fi
	done

	# Move files: setup script to /root; assets to canonical ASSETS_DIR=/home/ubuntu/.cache/exedops
	limactl shell ${LIMA_BASE} -- sudo mv /tmp/setup-containerd-clh-nydus.sh /root/setup-containerd-clh-nydus.sh
	limactl shell ${LIMA_BASE} -- sudo chmod +x /root/setup-containerd-clh-nydus.sh
	limactl shell ${LIMA_BASE} -- sudo mkdir -p /home/ubuntu/.cache/exedops
	limactl shell ${LIMA_BASE} -- sudo mv /tmp/kata-config-clh.toml /home/ubuntu/.cache/exedops/kata-config-clh.toml
	limactl shell ${LIMA_BASE} -- sudo bash -c 'for f in /tmp/*.tar.gz /tmp/*.tar.xz /tmp/*.tgz /tmp/*.tar /tmp/*.service /tmp/runc-* /tmp/ch-remote-static-*; do [ -e "$f" ] && mv "$f" /home/ubuntu/.cache/exedops/; done'

	echo "=========================================="
	echo "Starting containerd setup in VM"
	echo "=========================================="
	echo "Running setup script in VM (this will take a few minutes)..."
	# Set CI environment variable since Lima VMs are ephemeral-like
	# Set ALLOW_DEV_HOST_ACCESS to allow containers to access Mac host on port 8080 for development
	limactl shell ${LIMA_BASE} -- sudo CI=1 ALLOW_DEV_HOST_ACCESS=1 /root/setup-containerd-clh-nydus.sh

	echo "=========================================="
	echo "Lima-specific cofiguration..."
	echo "=========================================="
	# avahi-daemon makes *.local domains work; docker-registry runs a local registery, insecure (http), at port 5000
	limactl shell ${LIMA_BASE} sudo apt-get -y install avahi-daemon docker-registry
	# Copy default SSH keys to root's login, so ssh root@lima-exe-ctr.local works
	(cat ~/.ssh/id_*.pub | limactl shell ${LIMA_BASE} sudo tee /root/.authorized_keys) || true

	echo "Saving containerd configuration for persistence..."
	limactl shell ${LIMA_BASE} -- sudo cp /etc/containerd/config.toml /home/ubuntu/containerd-config.toml.backup 2>/dev/null || true
}

echo "=== Setting up Lima hosts for exe.dev containerd testing ==="

if ! command -v limactl &>/dev/null; then
	echo "Error: lima is not installed"
	echo "Install with: brew install lima"
	exit 1
fi

# Clean up existing instances if they exist
limactl stop --tty=false exe-ctr-base -f 2>/dev/null || true
limactl stop --tty=false exe-ctr -f 2>/dev/null || true
limactl stop --tty=false exe-ctr-tests -f 2>/dev/null || true

sleep 2

limactl delete exe-ctr-base --tty=false -f 2>/dev/null || true
limactl delete exe-ctr --tty=false -f 2>/dev/null || true
limactl delete exe-ctr-tests --tty=false -f 2>/dev/null || true

echo "Creating base Lima instance: ${LIMA_BASE}"
limactl create --tty=false --name=${LIMA_BASE} \
	--vm-type=vz \
	--cpus=${CPUS} \
	--memory=${MEMORY} \
	--network=vzNAT \
	--disk=${DISK} \
	--set ".nestedVirtualization=true" \
	template://ubuntu-24.04
limactl start --tty=false ${LIMA_BASE}

echo "Checking for KVM support in VM..."
if limactl shell ${LIMA_BASE} -- ls /dev/kvm 2>/dev/null; then
	echo "✓ KVM is available (/dev/kvm found) - Kata containers should work"
else
	echo "⚠️  KVM is not available (/dev/kvm not found) - Kata containers won't work"
	exit 1
fi

echo "Testing Lima SSH connection..."
limactl shell ${LIMA_BASE} -- echo "SSH connection successful"

# Provision the base VM
provision_base_vm

echo "Stopping base instance before cloning..."
limactl stop ${LIMA_BASE}

echo "Cloning ${LIMA_BASE} to ${LIMA_HOST_A}..."
limactl clone --tty=false ${LIMA_BASE} ${LIMA_HOST_A}

echo "Cloning ${LIMA_BASE} to ${LIMA_HOST_B}..."
limactl clone --tty=false ${LIMA_BASE} ${LIMA_HOST_B}

echo "Starting ${LIMA_HOST_A}..."
limactl start --tty=false ${LIMA_HOST_A}

echo "Starting ${LIMA_HOST_B}..."
limactl start --tty=false ${LIMA_HOST_B}

echo "Configuring SSH access..."
echo "Adding Lima SSH config includes..."
mkdir -p "$HOME/.ssh"
touch "$HOME/.ssh/config"

# Check if includes are already present
if ! grep -q "Include ~/.lima/\*/ssh.config" "$HOME/.ssh/config"; then
	# Add at the beginning of the file
	echo "Include ~/.lima/*/ssh.config" | cat - "$HOME/.ssh/config" >"$HOME/.ssh/config.tmp" && mv "$HOME/.ssh/config.tmp" "$HOME/.ssh/config"
	echo "✓ Added Lima SSH config includes"
else
	echo "✓ Lima SSH config includes already present"
fi

echo ""
echo "=========================================="
echo "Lima hosts ready"
echo "=========================================="
echo ""
echo "To access the VMs:"
echo "  ssh lima-exe-ctr          # Main host"
echo "  ssh lima-exe-ctr-tests    # Tests host"
echo ""
echo "To restore VMs to initial state:"
echo "  ${OPS_DIR}/reset-lima-hosts.sh"
echo ""
echo "=========================================="
