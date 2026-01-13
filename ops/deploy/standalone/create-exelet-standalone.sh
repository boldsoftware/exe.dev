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
        -h|--help)
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
        curl \
        docker.io \
        gdisk \
        libcap-ng0 \
        libseccomp2 \
        parted \
        socat \
        zfsutils-linux

    # Ensure docker is running
    systemctl enable docker
    systemctl start docker
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
    else
        echo "Building from source via Docker (this may take 10-20 minutes)..."

        local BUILD_DIR
        BUILD_DIR=$(mktemp -d)
        trap "rm -rf $BUILD_DIR" EXIT

        # Create Dockerfile
        cat >"$BUILD_DIR/Dockerfile" <<'DOCKERFILE'
# Build Cloud Hypervisor and virtiofsd binaries.

FROM rust:1.82-bullseye AS build

ARG CLOUD_HYPERVISOR_VERSION=48.0
ARG VIRTIOFSD_VERSION=1.13.2
ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    git \
    libcap-ng-dev \
    libseccomp-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN rustup toolchain install nightly --profile minimal && \
    rustup component add rustfmt --toolchain nightly && \
    rustup default nightly

WORKDIR /build

# Build Cloud Hypervisor
RUN curl -fsSL \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/archive/refs/tags/v${CLOUD_HYPERVISOR_VERSION}.tar.gz" \
        -o cloud-hypervisor.tar.gz && \
    tar xzf cloud-hypervisor.tar.gz

WORKDIR /build/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}

RUN cargo +nightly build --release

# Capture artifacts immediately to preserve permissions when reused from cache.
RUN install -Dm755 target/release/cloud-hypervisor /out/bin/cloud-hypervisor && \
    install -Dm755 target/release/ch-remote /out/bin/ch-remote

# Build virtiofsd
WORKDIR /build
RUN git clone --depth=1 --branch "v${VIRTIOFSD_VERSION}" \
        https://gitlab.com/virtio-fs/virtiofsd.git

WORKDIR /build/virtiofsd
RUN cargo +nightly build --release

RUN install -Dm755 target/release/virtiofsd /out/bin/virtiofsd

WORKDIR /out
RUN printf 'cloud_hypervisor_version=%s\nvirtiofsd_version=%s\narch=%s\n' \
        "${CLOUD_HYPERVISOR_VERSION}" \
        "${VIRTIOFSD_VERSION}" \
        "${TARGETARCH:-unknown}" > metadata

FROM scratch AS artifacts
COPY --from=build /out /out
CMD ["/out/bin/cloud-hypervisor"]
DOCKERFILE

        local IMAGE_TAG="exe-cloud-hypervisor:${CLOUD_HYPERVISOR_VERSION}-${ARCH}"

        docker build \
            --tag "$IMAGE_TAG" \
            --build-arg "CLOUD_HYPERVISOR_VERSION=${CLOUD_HYPERVISOR_VERSION}" \
            --build-arg "VIRTIOFSD_VERSION=${VIRTIOFSD_VERSION}" \
            --build-arg "TARGETARCH=${ARCH}" \
            "$BUILD_DIR"

        local CONTAINER_ID
        CONTAINER_ID=$(docker create "$IMAGE_TAG" /bin/true)
        local TMP_OUT
        TMP_OUT=$(mktemp -d)

        docker cp "$CONTAINER_ID:/out/." "$TMP_OUT"
        docker rm "$CONTAINER_ID" >/dev/null 2>&1 || true

        tar czf "$CACHE_DIR/$ARTIFACT_NAME" -C "$TMP_OUT" .
        rm -rf "$TMP_OUT"

        # Reset the trap since we're done with BUILD_DIR
        trap - EXIT
        rm -rf "$BUILD_DIR"

        echo "Cached Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} (${ARCH})"
    fi
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
