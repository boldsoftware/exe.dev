#!/bin/bash
set -euo pipefail

# Deploy rummyd to the mon host.
# Builds a linux/amd64 binary and deploys it as a systemd service.
#
# Usage:
#   cd observability && ./deploy-rummyd.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "=========================================="
echo "Deploying rummyd to mon"
echo "=========================================="
echo ""

# Build for linux/amd64
echo "Building rummyd..."
GOOS=linux GOARCH=amd64 go build -o "${SCRIPT_DIR}/rummyd" "${REPO_DIR}/cmd/rummyd"

# Copy binary
echo "Deploying binary..."
scp "${SCRIPT_DIR}/rummyd" ubuntu@mon:/tmp/rummyd
ssh ubuntu@mon "sudo mv /tmp/rummyd /usr/local/bin/rummyd && sudo chmod +x /usr/local/bin/rummyd"
rm -f "${SCRIPT_DIR}/rummyd"

# Deploy systemd service
echo "Deploying systemd service..."
ssh ubuntu@mon "sudo tee /etc/systemd/system/rummyd.service > /dev/null" <<'EOF'
[Unit]
Description=rummyd - Real User Monitoring daemon
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rummyd -listen :9099
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Reload and restart
echo "Reloading systemd and restarting rummyd..."
ssh ubuntu@mon "sudo systemctl daemon-reload && sudo systemctl enable rummyd && sudo systemctl restart rummyd"

# Verify
sleep 2
if ssh ubuntu@mon "curl -sf http://localhost:9099/healthz > /dev/null 2>&1"; then
    echo "Health check passed"
else
    echo "WARNING: Health check not responding yet"
    ssh ubuntu@mon "sudo systemctl status rummyd --no-pager" || true
fi

echo ""
echo "=========================================="
echo "rummyd deployment complete!"
echo "=========================================="
echo ""
echo "Metrics: http://mon:9099/metrics"
echo "Logs:    ssh ubuntu@mon journalctl -fu rummyd"
echo ""
echo "Don't forget to deploy prometheus config to scrape it:"
echo "  ./deploy-prometheus-config.sh"
