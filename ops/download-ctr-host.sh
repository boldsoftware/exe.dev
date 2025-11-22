#!/bin/bash
set -euo pipefail

# Check for arch parameter
if [ $# -ne 1 ]; then
    echo "Usage: $0 <arch>"
    echo "Where <arch> is either 'amd64' or 'arm64'"
    exit 1
fi

ARCH="$1"

# Validate arch
if [ "$ARCH" != "amd64" ] && [ "$ARCH" != "arm64" ]; then
    echo "Error: arch must be either 'amd64' or 'arm64'"
    exit 1
fi

# Configuration
CACHE_DIR="$HOME/.cache/exedops"
METADATA_DIR="$CACHE_DIR/.metadata"
CLOUD_HYPERVISOR_VERSION="48.0"
VIRTIOFSD_VERSION="1.13.2"

echo "=== Downloading exelet host dependencies for $ARCH ==="
echo "Cache directory: $CACHE_DIR"

# Create cache directory
mkdir -p "$CACHE_DIR"
mkdir -p "$METADATA_DIR"

# Function to download file if stale or not cached
download_if_needed() {
    local url="$1"
    local filename="$2"
    local filepath="$CACHE_DIR/$filename"
    local etag_file="${METADATA_DIR}/${filename}.etag"
    local header_file="${METADATA_DIR}/${filename}.headers.$$"
    local existing_etag=""
    local remote_etag=""
    local status_code=""

    if [ -f "$etag_file" ]; then
        existing_etag=$(tr -d '\r\n' <"$etag_file" || true)
    fi

    # Attempt conditional HEAD request when we have an existing file+etag
    if [ -f "$filepath" ] && [ -n "$existing_etag" ]; then
        status_code=$(curl -fsSLI \
            -H "If-None-Match: $existing_etag" \
            -D "$header_file" \
            -o /dev/null \
            -w '%{http_code}' \
            "$url" 2>/dev/null || true)

        if [ "$status_code" = "304" ]; then
            rm -f "$header_file"
            return 0
        fi

        if [ -n "$status_code" ] && [ -f "$header_file" ]; then
            remote_etag=$(grep -i '^etag:' "$header_file" | tail -n1 | cut -d' ' -f2- | tr -d '\r')
            rm -f "$header_file"
            if [ -n "$remote_etag" ] && [ "$remote_etag" = "$existing_etag" ]; then
                return 0
            fi
        fi
    fi

    echo "Downloading $filename..."
    local curl_args=(
        -fL
        --retry 3
        --retry-delay 2
        --connect-timeout 30
        -D "$header_file"
        -o "${filepath}.tmp"
    )

    if [ -n "$existing_etag" ]; then
        curl_args+=(-H "If-None-Match: $existing_etag")
    fi

    if curl "${curl_args[@]}" "$url"; then
        status_code=$(head -n 1 "$header_file" | awk '{print $2}')
        remote_etag=$(grep -i '^etag:' "$header_file" | tail -n1 | cut -d' ' -f2- | tr -d '\r')
        rm -f "$header_file"

        if [ "$status_code" = "304" ]; then
            rm -f "${filepath}.tmp"
            return 0
        fi

        mv "${filepath}.tmp" "$filepath"
        if [ -n "$remote_etag" ]; then
            printf '%s\n' "$remote_etag" >"$etag_file"
        else
            rm -f "$etag_file"
        fi
        echo "✓ $filename downloaded"
    else
        rm -f "${filepath}.tmp" "$header_file"
        echo "✗ Failed to download $filename"
        return 1
    fi
}

# Download cloud-hypervisor source (for building)
download_if_needed \
    "https://github.com/cloud-hypervisor/cloud-hypervisor/archive/refs/tags/v${CLOUD_HYPERVISOR_VERSION}.tar.gz" \
    "cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}.tar.gz"

# Download virtiofsd source (for building)
download_if_needed \
    "https://gitlab.com/virtio-fs/virtiofsd/-/archive/v${VIRTIOFSD_VERSION}/virtiofsd-${VIRTIOFSD_VERSION}.tar.gz" \
    "virtiofsd-${VIRTIOFSD_VERSION}.tar.gz"

echo ""
echo "=== Download complete ==="
echo "All files cached in: $CACHE_DIR"
