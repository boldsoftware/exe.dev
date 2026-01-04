#!/bin/bash
set -euo pipefail

ASSETS_DIR="/home/ubuntu/.cache/exedops"
DATA_DIR="/data/exelet"

echo "=== Running setup-exelet.sh ==="

# Determine architecture for binary selection
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    ARCH="amd64"
elif [ "$ARCH" = "aarch64" ]; then
    ARCH="arm64"
fi

EXELETD="${ASSETS_DIR}/exeletd-${ARCH}"
EXELET_CTL="${ASSETS_DIR}/exelet-ctl-${ARCH}"

if [ ! -f "$EXELETD" ]; then
    echo "ERROR: exeletd binary not found at $EXELETD"
    exit 1
fi

if [ ! -f "$EXELET_CTL" ]; then
    echo "ERROR: exelet-ctl binary not found at $EXELET_CTL"
    exit 1
fi

chmod +x "$EXELETD" "$EXELET_CTL"

# Images to preload
IMAGES=(
    "ghcr.io/boldsoftware/exeuntu:latest"
    "ghcr.io/linuxcontainers/alpine:latest"
    "docker.io/library/ubuntu:latest"
)

# Start bootstrap exelet
echo "Starting exeletd in background..."
mkdir -p "${DATA_DIR}"/{storage,network,runtime}
nohup "$EXELETD" \
    --stage test \
    --data-dir "${DATA_DIR}" \
    --storage-manager-address "zfs://${DATA_DIR}/storage?dataset=tank" \
    --network-manager-address "nat://${DATA_DIR}/network?network=10.42.0.0/16" \
    --runtime-address "cloudhypervisor://${DATA_DIR}/runtime" \
    --exed-url "http://127.0.0.1:9081" \
    --listen-address "tcp://127.0.0.1:9080" \
    --enable-hugepages >/tmp/exeletd.log 2>&1 &

EXELET_PID=$!
echo "Started exeletd with PID $EXELET_PID"

# Wait for exelet to be ready
echo "Waiting for exeletd to be ready..."
MAX_ATTEMPTS=60
attempt=0
until timeout 2 "$EXELET_CTL" compute instances ls >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    if [ $attempt -ge $MAX_ATTEMPTS ]; then
        echo "ERROR: exeletd failed to start after $MAX_ATTEMPTS seconds"
        echo "=== exeletd log ==="
        cat /tmp/exeletd.log || true
        kill $EXELET_PID 2>/dev/null || true
        exit 1
    fi
    echo "  attempt $attempt/$MAX_ATTEMPTS: waiting for exeletd..."
    sleep 1
done

echo "exeletd is ready"

# Load images into storage manager
for image in "${IMAGES[@]}"; do
    if ! "$EXELET_CTL" storage fs load "$image"; then
        echo "WARNING: Failed to load $image (may be rate limited)"
    fi
done

# Teardown bootstrap exelet
echo "Stopping exeletd..."
kill $EXELET_PID 2>/dev/null || true
wait $EXELET_PID 2>/dev/null || true

echo "=== setup-exelet.sh complete ==="
