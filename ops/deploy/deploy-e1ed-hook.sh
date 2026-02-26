#!/usr/bin/env bash
# Build e1ed-hook, copy to edric.
set -euo pipefail

echo "==========================================="
echo "Deploying e1ed-hook"
echo "==========================================="
echo ""

go mod verify

echo "Building binary..."
GOOS=linux GOARCH=amd64 go build -o /tmp/e1ed-hook ./cmd/e1ed-hook

echo "Deploying to edric..."
scp /tmp/e1ed-hook root@edric:/tmp/e1ed-hook
ssh root@edric 'mv /tmp/e1ed-hook /usr/local/bin/e1ed-hook && chmod +x /usr/local/bin/e1ed-hook'

rm /tmp/e1ed-hook

echo ""
echo "==========================================="
echo "Deployment Complete!"
echo "==========================================="
