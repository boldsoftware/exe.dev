#!/bin/bash
# Standalone script to set up an Ubuntu server as an exelet host.
# This installs cloud-hypervisor, virtiofsd, and configures the system.
#
# Usage: ./create-exelet-standalone.sh [OPTIONS]
#
# Options:
#   --data-device DEVICE   Block device to use for ZFS data pool (e.g., /dev/sdb)
#   --skip-zfs             Skip ZFS setup even if --data-device is provided
#
# Example:
#   ./create-exelet-standalone.sh --data-device /dev/sdb
#
set -euo pipefail

CLOUD_HYPERVISOR_VERSION="48.0"
VIRTIOFSD_VERSION="1.13.2"

DATA_DEVICE=""
SKIP_ZFS=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
    --data-device)
        DATA_DEVICE="$2"
        shift 2
        ;;
    --skip-zfs)
        SKIP_ZFS=true
        shift
        ;;
    -h | --help)
        echo "Usage: $0 [OPTIONS]"
        echo ""
        echo "Options:"
        echo "  --data-device DEVICE   Block device to use for ZFS data pool (e.g., /dev/sdb)"
        echo "  --skip-zfs             Skip ZFS setup even if --data-device is provided"
        exit 0
        ;;
    *)
        echo "Unknown option: $1" >&2
        exit 1
        ;;
    esac
done

ensure_root() {
    if [ "$(id -u)" -ne 0 ]; then
        echo "This script must be run as root" >&2
        exit 1
    fi
}

detect_arch() {
    case "$(uname -m)" in
    aarch64) echo "arm64" ;;
    x86_64) echo "amd64" ;;
    *)
        echo "Unsupported architecture: $(uname -m)" >&2
        exit 1
        ;;
    esac
}

install_packages() {
    echo "=== Installing required packages ==="
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        build-essential \
        curl \
        gdisk \
        git \
        libcap-ng-dev \
        libcap-ng0 \
        libseccomp-dev \
        libseccomp2 \
        parted \
        pkg-config \
        socat \
        zfsutils-linux
}

configure_sysctl() {
    echo "=== Configuring sysctl settings ==="
    cat <<EOF >/etc/sysctl.d/90-exe.conf
net.ipv4.neigh.default.gc_thresh1=4096
net.ipv4.neigh.default.gc_thresh2=8192
net.ipv4.neigh.default.gc_thresh3=16384
vm.max_map_count=1048576
EOF
    sysctl --system >/dev/null
}

setup_zfs() {
    if [ "$SKIP_ZFS" = true ]; then
        echo "=== Skipping ZFS setup ==="
        return
    fi

    if [ -z "$DATA_DEVICE" ]; then
        echo "=== No data device specified, skipping ZFS setup ==="
        echo "    Use --data-device /dev/sdX to set up ZFS pool"
        mkdir -p /data/exelet
        return
    fi

    if [ ! -b "$DATA_DEVICE" ]; then
        echo "ERROR: Data device $DATA_DEVICE does not exist or is not a block device" >&2
        exit 1
    fi

    echo "=== Setting up ZFS pool on $DATA_DEVICE ==="

    # Check if already a ZFS member
    local fs_type
    fs_type="$(blkid -o value -s TYPE "$DATA_DEVICE" 2>/dev/null || true)"
    if [ "$fs_type" = "zfs_member" ]; then
        echo "Device is already a ZFS member, importing pool..."
        zpool import tank 2>/dev/null || true
    else
        echo "Wiping existing filesystem signatures on $DATA_DEVICE..."
        # Unmount if mounted
        umount "$DATA_DEVICE" 2>/dev/null || true
        # Clear partition table and filesystem signatures
        wipefs -af "$DATA_DEVICE" >/dev/null 2>&1 || true
        sgdisk --zap-all "$DATA_DEVICE" >/dev/null 2>&1 || true
        # Zero out first and last MB to clear any remaining metadata
        dd if=/dev/zero of="$DATA_DEVICE" bs=1M count=1 2>/dev/null || true
        dd if=/dev/zero of="$DATA_DEVICE" bs=1M seek=$(($(blockdev --getsz "$DATA_DEVICE") / 2048 - 1)) count=1 2>/dev/null || true
        # Inform kernel of partition changes
        partprobe "$DATA_DEVICE" 2>/dev/null || true
        echo "Creating ZFS pool 'tank' on $DATA_DEVICE..."
        zpool create -f -m none tank "$DATA_DEVICE"
        zfs create -o mountpoint=/data tank/data
    fi

    mkdir -p /data/exelet
    echo "ZFS pool 'tank' is ready, mounted at /data"
}

build_cloud_hypervisor() {
    echo "=== Building Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} and virtiofsd ${VIRTIOFSD_VERSION} ==="

    local ARCH
    ARCH=$(detect_arch)
    local CACHE_DIR="/root/.cache/exedops"
    local ARTIFACT_NAME="cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}-${ARCH}.tar.gz"

    mkdir -p "$CACHE_DIR"

    # Check if we already have cached artifacts
    if [ -f "$CACHE_DIR/$ARTIFACT_NAME" ]; then
        echo "Using cached Cloud Hypervisor build"
        return
    fi

    echo "Building from source via cargo (this may take 10-20 minutes)..."

    # Install Rust nightly if not present
    if ! command -v rustup &>/dev/null; then
        curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain nightly
        # shellcheck disable=SC1091
        source "$HOME/.cargo/env"
    fi
    rustup toolchain install nightly --profile minimal
    rustup component add rustfmt --toolchain nightly
    rustup default nightly

    local BUILD_DIR
    BUILD_DIR=$(mktemp -d)
    trap "rm -rf $BUILD_DIR" EXIT

    local OUT_DIR="$BUILD_DIR/out"
    mkdir -p "$OUT_DIR/bin"

    # Build Cloud Hypervisor
    echo "Downloading cloud-hypervisor v${CLOUD_HYPERVISOR_VERSION}..."
    curl -fsSL \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/archive/refs/tags/v${CLOUD_HYPERVISOR_VERSION}.tar.gz" \
        -o "$BUILD_DIR/cloud-hypervisor.tar.gz"
    tar xzf "$BUILD_DIR/cloud-hypervisor.tar.gz" -C "$BUILD_DIR"

    echo "Building cloud-hypervisor..."
    cargo +nightly build --release --manifest-path "$BUILD_DIR/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}/Cargo.toml"
    install -m 0755 "$BUILD_DIR/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}/target/release/cloud-hypervisor" "$OUT_DIR/bin/cloud-hypervisor"
    install -m 0755 "$BUILD_DIR/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}/target/release/ch-remote" "$OUT_DIR/bin/ch-remote"

    # Build virtiofsd
    echo "Cloning virtiofsd v${VIRTIOFSD_VERSION}..."
    git clone --depth=1 --branch "v${VIRTIOFSD_VERSION}" \
        https://gitlab.com/virtio-fs/virtiofsd.git "$BUILD_DIR/virtiofsd"

    echo "Building virtiofsd..."
    cargo +nightly build --release --manifest-path "$BUILD_DIR/virtiofsd/Cargo.toml"
    install -m 0755 "$BUILD_DIR/virtiofsd/target/release/virtiofsd" "$OUT_DIR/bin/virtiofsd"

    # Write metadata
    printf 'cloud_hypervisor_version=%s\nvirtiofsd_version=%s\narch=%s\n' \
        "${CLOUD_HYPERVISOR_VERSION}" \
        "${VIRTIOFSD_VERSION}" \
        "${ARCH}" >"$OUT_DIR/metadata"

    tar czf "$CACHE_DIR/$ARTIFACT_NAME" -C "$OUT_DIR" .

    trap - EXIT
    rm -rf "$BUILD_DIR"

    echo "Cached Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} (${ARCH})"
}

install_cloud_hypervisor() {
    echo "=== Installing Cloud Hypervisor binaries ==="

    local ARCH
    ARCH=$(detect_arch)
    local CACHE_DIR="/root/.cache/exedops"
    local ARTIFACT_NAME="cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}-${ARCH}.tar.gz"
    local ARCHIVE="$CACHE_DIR/$ARTIFACT_NAME"

    if [ ! -f "$ARCHIVE" ]; then
        echo "ERROR: Cloud Hypervisor archive not found at $ARCHIVE" >&2
        exit 1
    fi

    local TMP_DIR
    TMP_DIR=$(mktemp -d)
    trap "rm -rf $TMP_DIR" EXIT

    tar xzf "$ARCHIVE" -C "$TMP_DIR"

    for bin in cloud-hypervisor ch-remote virtiofsd; do
        if [ ! -f "$TMP_DIR/bin/$bin" ]; then
            echo "ERROR: Archive missing $bin" >&2
            exit 1
        fi
    done

    install -m 0755 "$TMP_DIR/bin/cloud-hypervisor" /usr/local/bin/cloud-hypervisor
    install -m 0755 "$TMP_DIR/bin/ch-remote" /usr/local/bin/ch-remote
    install -m 0755 "$TMP_DIR/bin/virtiofsd" /usr/local/bin/virtiofsd

    trap - EXIT
    rm -rf "$TMP_DIR"

    echo "Installed Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} (${ARCH})"
}

verify_installation() {
    echo "=== Verifying installation ==="

    local failed=false

    if ! command -v cloud-hypervisor &>/dev/null; then
        echo "ERROR: cloud-hypervisor not found in PATH" >&2
        failed=true
    else
        echo "  cloud-hypervisor: $(cloud-hypervisor --version 2>&1 | head -1)"
    fi

    if ! command -v ch-remote &>/dev/null; then
        echo "ERROR: ch-remote not found in PATH" >&2
        failed=true
    else
        echo "  ch-remote: OK"
    fi

    if ! command -v virtiofsd &>/dev/null; then
        echo "ERROR: virtiofsd not found in PATH" >&2
        failed=true
    else
        echo "  virtiofsd: $(virtiofsd --version 2>&1 | head -1)"
    fi

    if [ "$failed" = true ]; then
        exit 1
    fi
}

print_summary() {
    echo ""
    echo "=========================================="
    echo "Exelet host setup complete!"
    echo "=========================================="
    echo ""
    echo "Installed components:"
    echo "  - Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION}"
    echo "  - virtiofsd ${VIRTIOFSD_VERSION}"
    echo "  - ch-remote"
    echo ""
    if [ -n "$DATA_DEVICE" ] && [ "$SKIP_ZFS" = false ]; then
        echo "ZFS pool 'tank' created on $DATA_DEVICE"
        echo "Data directory: /data"
    fi
    echo ""
    echo "The server is now ready to run exeletd."
    echo "=========================================="
}

main() {
    ensure_root
    install_packages
    configure_sysctl
    setup_zfs
    build_cloud_hypervisor
    install_cloud_hypervisor
    verify_installation
    print_summary
}

main
