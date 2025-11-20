#!/bin/bash
set -euo pipefail
set -E # inherit traps
trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR

# Configuration
LIMA_BASE="exe-ctr-base"
LIMA_HOST_A="exe-ctr"
LIMA_HOST_B="exe-ctr-tests"
DATA_DISK_SIZE="100GiB"

# Determine repo ops dir
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIMA_CONFIG_PATH="${OPS_DIR}/lima-with-data.yaml"
if [[ ! -f "$LIMA_CONFIG_PATH" ]]; then
    echo "Required Lima config not found: $LIMA_CONFIG_PATH" >&2
    exit 1
fi
PROVISION_SCRIPT_PATH="${OPS_DIR}/lima-provision.sh"
if [[ ! -f "$PROVISION_SCRIPT_PATH" ]]; then
    echo "Required Lima provision script not found: $PROVISION_SCRIPT_PATH" >&2
    exit 1
fi

LIMA_DIR="$HOME/.lima"
BOOTSTRAP_STAGING="/tmp/exe-bootstrap"

data_disk_name() {
    echo "data-$1"
}

data_disk_path() {
    local disk
    disk="$(data_disk_name "$1")"
    echo "${LIMA_DIR}/_disks/${disk}/datadisk"
}

set_disk_expr() {
    local disk="$1"
    printf '.additionalDisks[0].name = "%s"' "${disk}"
}

delete_data_disk() {
    local instance="$1"
    local disk
    disk="$(data_disk_name "$instance")"
    if limactl --tty=false disk delete "${disk}" >/dev/null 2>&1; then
        return 0
    fi
    rm -rf "${LIMA_DIR}/_disks/${disk}" >/dev/null 2>&1 || true
}

create_fresh_data_disk() {
    local instance="$1"
    local disk
    disk="$(data_disk_name "$instance")"
    echo "Creating Lima disk ${disk} (${DATA_DISK_SIZE})..."
    delete_data_disk "${instance}"
    limactl --tty=false --log-level=warn disk create "${disk}" --size "${DATA_DISK_SIZE}"
}

clone_data_disk() {
    local src_instance="$1"
    local dst_instance="$2"
    local src_disk
    src_disk="$(data_disk_name "$src_instance")"
    local dst_disk
    dst_disk="$(data_disk_name "$dst_instance")"
    local src_path
    src_path="$(data_disk_path "$src_instance")"
    if [[ ! -f "${src_path}" ]]; then
        echo "Error: source data disk not found at ${src_path}" >&2
        exit 1
    fi
    echo "Cloning Lima disk ${src_disk} -> ${dst_disk}..."
    delete_data_disk "${dst_instance}"
    limactl --tty=false --log-level=warn disk import "${dst_disk}" "${src_path}"
}

# Provision a fresh Lima VM with exelet + Cloud Hypervisor
provision_base_vm() {
    local script_dir="${OPS_DIR}"

    # Download dependencies locally if not cached
    VM_ARCH="arm64"
    echo "Ensuring dependencies are downloaded for $VM_ARCH..."
    "${script_dir}/download-ctr-host.sh" "$VM_ARCH"

    echo "Preparing bootstrap assets for VM..."
    limactl shell ${LIMA_BASE} -- sudo rm -rf "${BOOTSTRAP_STAGING}"
    limactl shell ${LIMA_BASE} -- sudo mkdir -p "${BOOTSTRAP_STAGING}"
    limactl shell ${LIMA_BASE} -- sudo chmod 1777 "${BOOTSTRAP_STAGING}"

    echo "Copying cloud-hypervisor and virtiofsd sources to VM..."
    CACHE_DIR="$HOME/.cache/exedops"

    for file in "$CACHE_DIR"/cloud-hypervisor-*.tar.gz "$CACHE_DIR"/virtiofsd-*.tar.gz "$CACHE_DIR"/*.tar; do
        if [ -f "$file" ]; then
            basename=$(basename "$file")
            echo "  Copying $basename..."
            limactl cp "$file" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/$basename"
        fi
    done

    # cloud hypervisor setup script
    limactl cp "${script_dir}/setup-cloud-hypervisor.sh" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/setup-cloud-hypervisor.sh"

    # build and cache a local exelet to be able to provision the base instance volumes
    echo "Building bootstrap exelet..."
    make GOOS=linux GOARCH=${VM_ARCH} exelet exelet-ctl
    limactl cp "exeletd" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/exeletd-${VM_ARCH}"
    limactl cp "exelet-ctl" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/exelet-ctl-${VM_ARCH}"
    limactl cp "${script_dir}/setup-exelet.sh" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/setup-exelet.sh"

    echo "Running bootstrap script in VM (this will take a few minutes)..."
    limactl shell ${LIMA_BASE} -- sudo bash /usr/local/bin/lima-provision.sh bootstrap

    # Copy default SSH keys to root's login, so ssh root@lima-exe-ctr.local works
    (cat ~/.ssh/id_*.pub | limactl shell ${LIMA_BASE} sudo tee /root/.authorized_keys) || true
}

echo "=== Setting up Lima hosts for exe.dev testing ==="

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

delete_data_disk "${LIMA_BASE}"
delete_data_disk "${LIMA_HOST_A}"
delete_data_disk "${LIMA_HOST_B}"

create_fresh_data_disk "${LIMA_BASE}"

echo "Creating base Lima instance: ${LIMA_BASE}"
base_disk_name="$(data_disk_name "${LIMA_BASE}")"
# Ensure mount location referenced by template exists
mkdir -p /tmp/lima
limactl create --plain --tty=false --log-level=warn --name=${LIMA_BASE} \
    --set "$(set_disk_expr "${base_disk_name}")" \
    "${LIMA_CONFIG_PATH}"
limactl start --tty=false --log-level=warn ${LIMA_BASE}

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
limactl stop --log-level=warn ${LIMA_BASE}

echo "Cloning ${LIMA_BASE} to ${LIMA_HOST_A}..."
limactl clone --tty=false --log-level=warn --set "$(set_disk_expr "$(data_disk_name "${LIMA_HOST_A}")")" ${LIMA_BASE} ${LIMA_HOST_A}

echo "Cloning ${LIMA_BASE} to ${LIMA_HOST_B}..."
limactl clone --tty=false --log-level=warn --set "$(set_disk_expr "$(data_disk_name "${LIMA_HOST_B}")")" ${LIMA_BASE} ${LIMA_HOST_B}

clone_data_disk "${LIMA_BASE}" "${LIMA_HOST_A}"
clone_data_disk "${LIMA_BASE}" "${LIMA_HOST_B}"

echo "Starting ${LIMA_HOST_A}..."
limactl start --log-level=warn --tty=false ${LIMA_HOST_A}

echo "Starting ${LIMA_HOST_B}..."
limactl start --log-level=warn --tty=false ${LIMA_HOST_B}

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

# Add IdentityFile configuration for .local hosts
if ! grep -q "Host lima-exe-ctr.local" "$HOME/.ssh/config"; then
    USER=$(whoami)
    cat >>"$HOME/.ssh/config" <<EOF

Host lima-exe-ctr.local
    IdentityFile /Users/${USER}/.lima/_config/user

Host lima-exe-ctr-tests.local
    IdentityFile /Users/${USER}/.lima/_config/user
EOF
    echo "✓ Added IdentityFile configuration for lima-exe-ctr.local and lima-exe-ctr-tests.local"
else
    echo "✓ IdentityFile configuration for .local hosts already present"
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
