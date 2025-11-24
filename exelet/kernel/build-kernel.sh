#!/bin/bash
set -euo pipefail

# Build kernel using Kata's official build-kernel.sh with nftables fragment

KERNEL_VERSION="${KERNEL_VERSION:-6.12.42}"
ARCH=$(uname -m)

echo "Building kernel $KERNEL_VERSION for $ARCH..."

cd /workspace/kata-containers/tools/packaging/kernel

# Setup kernel source
echo "Setting up kernel source..."
./build-kernel.sh -v "$KERNEL_VERSION" -f -a "$ARCH" -t clh -d setup

# Find kernel source directory
KERNEL_SRC=$(find /workspace/kata-containers/tools/packaging/kernel -maxdepth 1 -name "kata-linux-*" -type d | head -1)
if [ -z "$KERNEL_SRC" ]; then
    echo "ERROR: Could not find kata-linux source directory" >&2
    exit 1
fi

echo "Found kernel source at: $KERNEL_SRC"

# Apply nftables config fragment
echo "Applying nftables config fragment..."
cd "$KERNEL_SRC"
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/nftables.conf

echo "Applying kvm config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/kvm.conf

make olddefconfig

# Build kernel
echo "Building kernel..."
cd /workspace/kata-containers/tools/packaging/kernel
./build-kernel.sh -v "$KERNEL_VERSION" -a "$ARCH" -t clh build

# Prepare output
echo "Preparing output..."
mkdir -p /output

if [ "$ARCH" = "aarch64" ]; then
    cp "$KERNEL_SRC/arch/arm64/boot/Image" "/output/vmlinux-${KERNEL_VERSION}-nftables"
elif [ "$ARCH" = "x86_64" ]; then
    cp "$KERNEL_SRC/arch/x86/boot/bzImage" "/output/vmlinux-${KERNEL_VERSION}-nftables"
else
    echo "ERROR: Unsupported architecture: $ARCH" >&2
    exit 1
fi

cp "$KERNEL_SRC/.config" "/output/config-${KERNEL_VERSION}-nftables"

# Verify nftables is enabled
if grep -q "CONFIG_NF_TABLES=y" "/output/config-${KERNEL_VERSION}-nftables"; then
    echo "✓ nftables enabled in kernel config"
else
    echo "✗ nftables NOT enabled - build failed" >&2
    exit 1
fi

echo "Kernel build complete!"
ls -lh /output/
