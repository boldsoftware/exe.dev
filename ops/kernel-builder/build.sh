#!/bin/bash
set -euo pipefail

# Build custom kernel with nftables support for Cloud Hypervisor

KERNEL_VERSION="6.12.42"
OUTPUT_DIR="output"

cd "$(dirname "$0")"

echo "Building kernel $KERNEL_VERSION..."
mkdir -p "$OUTPUT_DIR"
docker build \
    --build-arg KERNEL_VERSION="$KERNEL_VERSION" \
    --output type=local,dest="$OUTPUT_DIR" \
    .

echo ""
echo "Kernel built successfully!"
echo "Output files:"
ls -lh "$OUTPUT_DIR/"
