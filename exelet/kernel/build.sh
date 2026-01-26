#!/bin/bash
set -euo pipefail

# Build custom kernel with nftables support for Cloud Hypervisor

KERNEL_VERSION="6.12.67"
OUTPUT_DIR="output"
IMAGE_TAG="exe-kernel-builder:${KERNEL_VERSION}"
container_id=""

cd "$(dirname "$0")"

echo "Building kernel $KERNEL_VERSION..."
mkdir -p "$OUTPUT_DIR"
rm -f "$OUTPUT_DIR"/*

cleanup() {
    if [[ -n "$container_id" ]]; then
        docker rm "$container_id" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

docker build \
    --build-arg KERNEL_VERSION="$KERNEL_VERSION" \
    -t "$IMAGE_TAG" \
    .

# Provide a no-op command because the scratch export image has no default CMD.
container_id="$(docker create "$IMAGE_TAG" /bin/true)"
docker cp "${container_id}:." "$OUTPUT_DIR" >/dev/null

echo ""
echo "Kernel built successfully!"
echo "Output files:"
ls -lh "$OUTPUT_DIR/"
