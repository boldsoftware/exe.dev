#!/bin/bash
set -euo pipefail

# Deploy rollcalld to the mon host.
# Builds a linux/amd64 binary and deploys it as a systemd service.
#
# Usage:
#   cd observability && ./deploy-rollcalld.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "=========================================="
echo "Deploying rollcalld to mon"
echo "=========================================="
echo ""

# Build for linux/amd64
echo "Building rollcalld..."
GOOS=linux GOARCH=amd64 go build -o "${SCRIPT_DIR}/rollcalld" "${REPO_DIR}/cmd/rollcalld"

# Copy binary
echo "Deploying binary..."
scp "${SCRIPT_DIR}/rollcalld" ubuntu@mon:/tmp/rollcalld
ssh ubuntu@mon "sudo mv /tmp/rollcalld /usr/local/bin/rollcalld && sudo chmod +x /usr/local/bin/rollcalld"
rm -f "${SCRIPT_DIR}/rollcalld"

# Deploy systemd service
echo "Deploying systemd service..."
ssh ubuntu@mon "sudo tee /etc/systemd/system/rollcalld.service > /dev/null" <<'EOF'
[Unit]
Description=rollcalld - service discovery gap detector
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rollcalld -listen :9098
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Reload and restart
echo "Reloading systemd and restarting rollcalld..."
ssh ubuntu@mon "sudo systemctl daemon-reload && sudo systemctl enable rollcalld && sudo systemctl restart rollcalld"

# Verify
sleep 2
if ssh ubuntu@mon "curl -sf http://localhost:9098/healthz > /dev/null 2>&1"; then
    echo "Health check passed"
else
    echo "WARNING: Health check not responding yet"
    ssh ubuntu@mon "sudo systemctl status rollcalld --no-pager" || true
fi

echo ""
echo "=========================================="
echo "rollcalld deployment complete!"
echo "=========================================="
echo ""
echo "Metrics: http://mon:9098/metrics"
echo "Logs:    ssh ubuntu@mon journalctl -fu rollcalld"
echo ""
echo "Don't forget to deploy prometheus config to scrape it:"
echo "  ./deploy-prometheus-config.sh"
echo ""
