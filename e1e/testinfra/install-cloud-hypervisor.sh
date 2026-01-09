#!/bin/bash
set -euo pipefail

# This script downloads the cloud-hypervisor static binary for e1e tests.
# Version should match ops/deploy/setup-exelet-host.sh and ops/setup-lima-hosts.sh

CLOUD_HYPERVISOR_VERSION="48.0"

if command -v cloud-hypervisor &>/dev/null; then
    echo "cloud-hypervisor already installed"
    exit 0
fi

echo "Downloading cloud-hypervisor v${CLOUD_HYPERVISOR_VERSION}..."

URL="https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v${CLOUD_HYPERVISOR_VERSION}/cloud-hypervisor-static"
TMP_FILE="/tmp/cloud-hypervisor-download"

curl -fsSL -o "$TMP_FILE" "$URL"
chmod +x "$TMP_FILE"
sudo mv "$TMP_FILE" /usr/local/bin/cloud-hypervisor

echo "cloud-hypervisor installed"
