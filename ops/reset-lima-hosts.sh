#!/bin/bash
set -euo pipefail

# Configuration - must match setup-lima-hosts.sh
LIMA_BASE="exe-ctr-base"
LIMA_HOST_A="exe-ctr"
LIMA_HOST_B="exe-ctr-tests"

echo "=== Resetting Lima hosts to initial state ==="

if ! command -v limactl &>/dev/null; then
    echo "Error: lima is not installed"
    echo "Install with: brew install lima"
    exit 1
fi

# Check if base instance exists
if ! limactl list | grep "${LIMA_BASE}" >/dev/null 2>&1; then
    echo "Error: Base instance ${LIMA_BASE} not found"
    echo "Please run ./ops/setup-lima-hosts.sh first to create the base instance"
    exit 1
fi

LIMA_DIR="$HOME/.lima"
BOOTSTRAP_STAGING="/tmp/exe-bootstrap"

# Determine repo ops dir
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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
    limactl --tty=false disk import "${dst_disk}" "${src_path}"
}

provision_cloned_vm() {
    local instance="$1"
    local script_dir="${OPS_DIR}"

    echo "Provisioning cloned instance ${instance}..."

    # Prepare bootstrap assets for VM
    limactl shell ${instance} -- sudo rm -rf "${BOOTSTRAP_STAGING}"
    limactl shell ${instance} -- sudo mkdir -p "${BOOTSTRAP_STAGING}"
    limactl shell ${instance} -- sudo chmod 1777 "${BOOTSTRAP_STAGING}"

    limactl cp "${script_dir}/setup-containerd-clh-nydus.sh" "${instance}:${BOOTSTRAP_STAGING}/setup-containerd-clh-nydus.sh"
    limactl cp "${script_dir}/kata-config-clh.toml" "${instance}:${BOOTSTRAP_STAGING}/kata-config-clh.toml"

    # Copy custom kernel if available
    KERNEL_BUILDER_DIR="${script_dir}/kernel-builder/output"
    if [ -f "${KERNEL_BUILDER_DIR}/vmlinux-6.12.42-nftables" ]; then
        limactl cp "${KERNEL_BUILDER_DIR}/vmlinux-6.12.42-nftables" "${instance}:${BOOTSTRAP_STAGING}/vmlinux-6.12.42-nftables"
        limactl cp "${KERNEL_BUILDER_DIR}/config-6.12.42-nftables" "${instance}:${BOOTSTRAP_STAGING}/config-6.12.42-nftables"
    fi

    # Copy pre-downloaded dependencies
    CACHE_DIR="$HOME/.cache/exedops"
    for file in "$CACHE_DIR"/*.tar.gz "$CACHE_DIR"/*.tar.xz "$CACHE_DIR"/*.tgz "$CACHE_DIR"/*.service "$CACHE_DIR"/runc-* "$CACHE_DIR"/ch-remote-static-* "$CACHE_DIR"/*.tar; do
        if [ -f "$file" ]; then
            basename=$(basename "$file")
            limactl cp "$file" "${instance}:${BOOTSTRAP_STAGING}/$basename"
        fi
    done

    # Run bootstrap script
    limactl shell ${instance} -- sudo bash /usr/local/bin/lima-provision.sh bootstrap
}

echo "Stopping instances..."
limactl stop --tty=false ${LIMA_BASE} -f 2>/dev/null || true
limactl stop --tty=false ${LIMA_HOST_A} -f 2>/dev/null || true
limactl stop --tty=false ${LIMA_HOST_B} -f 2>/dev/null || true

sleep 2

echo "Removing cloned instances..."
limactl delete ${LIMA_HOST_A} --tty=false -f 2>/dev/null || true
limactl delete ${LIMA_HOST_B} --tty=false -f 2>/dev/null || true

# Clean up cloned data disks
delete_data_disk "${LIMA_HOST_A}"
delete_data_disk "${LIMA_HOST_B}"

echo "Re-cloning from base..."
echo "Cloning ${LIMA_BASE} to ${LIMA_HOST_A}..."
limactl clone --tty=false --set "$(set_disk_expr "$(data_disk_name "${LIMA_HOST_A}")")" ${LIMA_BASE} ${LIMA_HOST_A}

echo "Cloning ${LIMA_BASE} to ${LIMA_HOST_B}..."
limactl clone --tty=false --set "$(set_disk_expr "$(data_disk_name "${LIMA_HOST_B}")")" ${LIMA_BASE} ${LIMA_HOST_B}

echo "Importing cloned data disks..."
clone_data_disk "${LIMA_BASE}" "${LIMA_HOST_A}"
clone_data_disk "${LIMA_BASE}" "${LIMA_HOST_B}"

echo "Starting cloned instances..."
limactl start --tty=false ${LIMA_HOST_A}
limactl start --tty=false ${LIMA_HOST_B}

echo "Provisioning cloned instances..."
provision_cloned_vm "${LIMA_HOST_A}"
provision_cloned_vm "${LIMA_HOST_B}"

echo ""
echo "=========================================="
echo "Lima hosts restored to initial state"
echo "=========================================="
echo ""
