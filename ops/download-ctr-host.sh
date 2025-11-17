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

# Download cloud-hypervisor remote binary
# Map arch to cloud-hypervisor naming (aarch64 for arm64, x86_64 for amd64)
if [ "$ARCH" = "arm64" ]; then
    CH_ARCH="aarch64"
    download_if_needed \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v${CLOUD_HYPERVISOR_VERSION}/ch-remote-static-aarch64" \
        "ch-remote-static-${CLOUD_HYPERVISOR_VERSION}-${ARCH}"
else
    CH_ARCH="x86_64"
    download_if_needed \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v${CLOUD_HYPERVISOR_VERSION}/ch-remote-static" \
        "ch-remote-static-${CLOUD_HYPERVISOR_VERSION}-${ARCH}"
fi

# Download cloud-hypervisor source (for building)
download_if_needed \
    "https://github.com/cloud-hypervisor/cloud-hypervisor/archive/refs/tags/v${CLOUD_HYPERVISOR_VERSION}.tar.gz" \
    "cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}.tar.gz"

# Download virtiofsd source (for building)
download_if_needed \
    "https://gitlab.com/virtio-fs/virtiofsd/-/archive/v${VIRTIOFSD_VERSION}/virtiofsd-${VIRTIOFSD_VERSION}.tar.gz" \
    "virtiofsd-${VIRTIOFSD_VERSION}.tar.gz"

echo ""
echo "=== Downloading container images to cache ==="

# Images to cache
IMAGES=(
    "ghcr.io/boldsoftware/exeuntu:latest"
    "docker.io/library/ubuntu:latest"
    "ghcr.io/linuxcontainers/alpine:latest"
)

#############################################
# Container image prefetch is REQUIRED here #
#############################################
# We want the host to download images once into cache so VMs don't re-pull.
# Require Docker (and jq for JSON parsing) for digest checks and saving.

# Detect if docker needs sudo
DOCKER_CMD="docker"
if docker ps >/dev/null 2>&1; then
    DOCKER_CMD="docker"
elif sudo docker ps >/dev/null 2>&1; then
    DOCKER_CMD="sudo docker"
else
    echo "ERROR: 'docker' is required for image caching." >&2
    echo "       Please install Docker and re-run." >&2
    if command -v colima >/dev/null 2>&1; then
        if ! colima status >/dev/null 2>&1; then
            echo "       Colima detected but not running. Start it with: colima start" >&2
        fi
    fi
    exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: 'jq' is required to parse 'docker manifest inspect' output." >&2
    echo "       Install jq and re-run (e.g., apt-get install -y jq or brew install jq)." >&2
    exit 1
fi

get_remote_digest() {
    local img="$1"
    local arch="$2"
    # Returns platform-specific manifest digest for linux/$arch
    go run github.com/google/go-containerregistry/cmd/crane@latest digest --platform=linux/$arch "$img" 2>/dev/null
}

for image in "${IMAGES[@]}"; do
    image_base=$(echo "$image" | sed 's|/|_|g' | sed 's|:|_|g')
    base_tar="$CACHE_DIR/${image_base}-${ARCH}.tar"
    digest_file="$CACHE_DIR/${image_base}-${ARCH}.digest"

    echo "Checking remote digest for $image (linux/$ARCH)..."
    remote_digest=$(get_remote_digest "$image" "$ARCH")
    if [ -z "$remote_digest" ]; then
        echo "✗ Failed to determine remote digest for $image (linux/$ARCH)" >&2
        exit 1
    fi
    echo "  Remote digest: $remote_digest"

    need_download=1
    if [ -f "$base_tar" ] && [ -f "$digest_file" ]; then
        cached_digest=$(cat "$digest_file" 2>/dev/null || true)
        if [ "$cached_digest" = "$remote_digest" ]; then
            echo "✓ ${image_base}-${ARCH}.tar up-to-date (digest matches)"
            need_download=0
        else
            echo "↻ ${image_base}-${ARCH}.tar stale (digest changed); refreshing..."
            rm -f "$base_tar" "$digest_file"
        fi
    elif [ -f "$base_tar" ] && [ ! -f "$digest_file" ]; then
        echo "↻ ${image_base}-${ARCH}.tar missing digest metadata; refreshing to ensure correctness..."
        rm -f "$base_tar"
    fi

    if [ $need_download -eq 1 ]; then
        echo "Downloading $image for linux/$ARCH..."
        if $DOCKER_CMD pull --platform="linux/$ARCH" "$image"; then
            echo "  Saving to tar..."
            if $DOCKER_CMD save "$image" >"$base_tar"; then
                echo "$remote_digest" >"$digest_file"
                echo "  ✓ Saved $base_tar with digest $remote_digest"
            else
                echo "  ✗ Failed to save $image to $base_tar" >&2
                rm -f "$base_tar" "$digest_file"
                exit 1
            fi
        else
            echo "  ✗ Failed to pull $image with docker" >&2
            exit 1
        fi
    fi
done

echo ""
echo "=== Download complete ==="
echo "All files cached in: $CACHE_DIR"
echo ""
echo "Cloud Hypervisor and virtiofsd sources:"
ls -lh "$CACHE_DIR" | grep -E "(cloud-hypervisor|virtiofsd|ch-remote)" | awk '{print "  - " $9 " (" $5 ")"}'
echo ""
echo "Container images:"
ls -lh "$CACHE_DIR" | grep -E "\.tar$" | awk '{print "  - " $9 " (" $5 ")"}' || echo "  None cached yet"
