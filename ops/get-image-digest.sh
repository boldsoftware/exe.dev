#!/usr/bin/env bash
set -euo pipefail

# Fetches the digest hash for a container image from GHCR
# Usage: get-image-digest.sh <image> <arch>
# Example: get-image-digest.sh ghcr.io/boldsoftware/exeuntu:latest amd64

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <image> <arch>" >&2
    echo "Example: $0 ghcr.io/boldsoftware/exeuntu:latest amd64" >&2
    exit 1
fi

IMAGE="$1"
ARCH="$2"

# Parse image into registry, repository, and tag
if [[ "$IMAGE" =~ ^([^/]+)/(.+):([^:]+)$ ]]; then
    REGISTRY="${BASH_REMATCH[1]}"
    REPO="${BASH_REMATCH[2]}"
    TAG="${BASH_REMATCH[3]}"
else
    echo "Invalid image format: $IMAGE" >&2
    echo "Expected format: registry/org/repo:tag" >&2
    exit 1
fi

# Get authentication token
TOKEN=$(curl -sL "https://${REGISTRY}/token?scope=repository:${REPO}:pull" | jq -r '.token')

if [[ -z "$TOKEN" || "$TOKEN" == "null" ]]; then
    echo "Failed to get authentication token" >&2
    exit 1
fi

# Fetch the manifest index
MANIFEST=$(curl -sL \
    -H "Authorization: Bearer $TOKEN" \
    -H "Accept: application/vnd.oci.image.index.v1+json" \
    "https://${REGISTRY}/v2/${REPO}/manifests/${TAG}")

# Extract digest for the specified architecture using jq
DIGEST=$(echo "$MANIFEST" | jq -r --arg arch "$ARCH" '
    .manifests[] |
    select(.platform.architecture == $arch and .platform.os == "linux") |
    .digest' | head -1)

if [[ -z "$DIGEST" || "$DIGEST" == "null" ]]; then
    echo "Failed to find digest for architecture: $ARCH" >&2
    exit 1
fi

echo "$DIGEST"
