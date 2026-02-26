#!/usr/bin/env bash
# Build e1ed, copy to edric, restart the systemd service.
set -euo pipefail

echo "==========================================="
echo "Deploying e1ed"
echo "==========================================="
echo ""

go mod verify

echo "Building binary..."
GOOS=linux GOARCH=amd64 go build -o /tmp/e1ed ./cmd/e1ed

echo "Deploying to edric..."
scp /tmp/e1ed root@edric:/tmp/e1ed
ssh root@edric 'mv /tmp/e1ed /usr/local/bin/e1ed && chmod +x /usr/local/bin/e1ed && systemctl restart e1ed'
ssh root@edric systemctl status e1ed --no-pager

rm /tmp/e1ed

echo ""
echo "==========================================="
echo "Deployment Complete!"
echo "==========================================="
