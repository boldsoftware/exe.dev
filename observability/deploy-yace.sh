#!/bin/bash
set -euo pipefail

# Deploy YACE (Yet Another CloudWatch Exporter) to the mon host.
# This script is idempotent - safe to run multiple times.
# It will upgrade YACE if a newer version is available.

YACE_PORT=5000
YACE_CONFIG_PATH=/etc/yace/config.yml
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the latest version from GitHub
echo "Checking latest YACE version..."
LATEST_VERSION=$(curl -s https://api.github.com/repos/prometheus-community/yet-another-cloudwatch-exporter/releases/latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

if [ -z "$LATEST_VERSION" ]; then
    echo "ERROR: Could not determine latest YACE version"
    exit 1
fi

echo "Latest YACE version: ${LATEST_VERSION}"

# Check current installed version on mon
CURRENT_VERSION=$(ssh ubuntu@mon "yace --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' || echo 'none'")
echo "Current installed version: ${CURRENT_VERSION}"

if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
    echo "YACE is already at latest version ${LATEST_VERSION}"
    NEEDS_INSTALL=false
else
    echo "Will install/upgrade YACE to ${LATEST_VERSION}"
    NEEDS_INSTALL=true
fi

if [ "$NEEDS_INSTALL" = true ]; then
    # Download and install on mon
    DOWNLOAD_URL="https://github.com/prometheus-community/yet-another-cloudwatch-exporter/releases/download/v${LATEST_VERSION}/yet-another-cloudwatch-exporter-${LATEST_VERSION}.linux-amd64.tar.gz"

    echo "Downloading and installing YACE ${LATEST_VERSION} on mon..."
    ssh ubuntu@mon "bash -s" <<EOF
set -euo pipefail
cd /tmp
curl -sLO "${DOWNLOAD_URL}"
tar xzf "yet-another-cloudwatch-exporter-${LATEST_VERSION}.linux-amd64.tar.gz"
sudo mv "yet-another-cloudwatch-exporter-${LATEST_VERSION}.linux-amd64/yace" /usr/local/bin/yace
sudo chmod +x /usr/local/bin/yace
rm -rf "yet-another-cloudwatch-exporter-${LATEST_VERSION}.linux-amd64" "yet-another-cloudwatch-exporter-${LATEST_VERSION}.linux-amd64.tar.gz"
echo "Installed: \$(/usr/local/bin/yace --version)"
EOF
fi

# Deploy config file
echo "Deploying YACE config..."
ssh ubuntu@mon "sudo mkdir -p /etc/yace"
scp "${SCRIPT_DIR}/yace-config.yml" ubuntu@mon:/tmp/yace-config.yml
ssh ubuntu@mon "sudo mv /tmp/yace-config.yml ${YACE_CONFIG_PATH} && sudo chmod 644 ${YACE_CONFIG_PATH}"

# Create/update systemd service
echo "Creating systemd service..."
ssh ubuntu@mon "sudo tee /etc/systemd/system/yace.service > /dev/null" <<EOF
[Unit]
Description=Yet Another CloudWatch Exporter
Documentation=https://github.com/prometheus-community/yet-another-cloudwatch-exporter
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/yace --config.file=${YACE_CONFIG_PATH} --listen-address=:${YACE_PORT}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Reload and restart
echo "Reloading systemd and restarting YACE..."
ssh ubuntu@mon "sudo systemctl daemon-reload && sudo systemctl enable yace && sudo systemctl restart yace"

# Verify it's running
echo "Verifying YACE is running..."
sleep 2
ssh ubuntu@mon "curl -s http://localhost:${YACE_PORT}/metrics | head -5" || {
    echo "ERROR: YACE does not appear to be running"
    ssh ubuntu@mon "sudo systemctl status yace"
    exit 1
}

echo ""
echo "=========================================="
echo "YACE deployment complete!"
echo "=========================================="
echo ""
echo "YACE is running on mon:${YACE_PORT}"
echo "Config file: ${YACE_CONFIG_PATH}"
echo ""
echo "Don't forget to run 'make deploy-prometheus' to update Prometheus config"
echo "=========================================="
