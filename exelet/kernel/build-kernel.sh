#!/bin/bash
set -euo pipefail

# Build kernel using Kata's official build-kernel.sh with nftables and ZFS support

KERNEL_VERSION="${KERNEL_VERSION:-6.12.67}"
ZFS_VERSION="${ZFS_VERSION:-zfs-2.2.9}"
ARCH=$(uname -m)

echo "Building kernel $KERNEL_VERSION with ZFS $ZFS_VERSION for $ARCH..."

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

# Prepare kernel source for ZFS
echo "Preparing kernel source..."
cd "$KERNEL_SRC"
make prepare

# Clone and prepare ZFS for kernel builtin
echo "Cloning OpenZFS $ZFS_VERSION..."
cd /workspace
git clone --depth=1 --branch="$ZFS_VERSION" https://github.com/openzfs/zfs.git

echo "Configuring ZFS for kernel builtin..."
cd /workspace/zfs
autoreconf -fi
./configure --enable-linux-builtin --with-linux="$KERNEL_SRC" --with-linux-obj="$KERNEL_SRC"

echo "Copying ZFS into kernel tree..."
./copy-builtin "$KERNEL_SRC"

# Apply config fragments
echo "Applying nftables config fragment..."
cd "$KERNEL_SRC"
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/nftables.conf

echo "Applying kvm config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/kvm.conf

echo "Applying zfs config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/zfs.conf

echo "Applying Landlock config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/landlock.conf

echo "Applying WireGuard config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/wireguard.conf

echo "Applying eBPF config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/ebpf.conf

echo "Applying devmapper config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/devmapper.conf

echo "Applying PSI config fragment..."
scripts/kconfig/merge_config.sh .config /workspace/kata-containers/tools/packaging/kernel/configs/fragments/psi.conf

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

# Verify ZFS is enabled
if grep -q "CONFIG_ZFS=y" "/output/config-${KERNEL_VERSION}-nftables"; then
    echo "✓ ZFS enabled in kernel config"
else
    echo "✗ ZFS NOT enabled - build failed" >&2
    exit 1
fi

# Verify WireGuard is enabled
if grep -q "CONFIG_WIREGUARD=y" "/output/config-${KERNEL_VERSION}-nftables"; then
    echo "✓ WireGuard enabled in kernel config"
else
    echo "✗ WireGuard NOT enabled - build failed" >&2
    exit 1
fi

# Verify PSI is enabled
if grep -q "CONFIG_PSI=y" "/output/config-${KERNEL_VERSION}-nftables"; then
    echo "✓ PSI enabled in kernel config"
else
    echo "✗ PSI NOT enabled - build failed" >&2
    exit 1
fi

echo "Kernel build complete!"
ls -lh /output/
