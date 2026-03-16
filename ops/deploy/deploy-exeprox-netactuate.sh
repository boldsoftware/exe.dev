#!/bin/bash
# Deploy exeprox binary to a NetActuate VM.
# Builds the binary locally and deploys via Tailscale SSH.
#
# These machines are connected to exed production but are NOT yet carrying
# traffic (no IP shards point at them). This script intentionally skips
# prodlock, Slack notifications, and check-deploy-safety so that deploying
# here cannot interfere with existing prod/staging machines.

set -euo pipefail

if [ $# -ne 1 ] || [ -z "$1" ]; then
    echo "Usage: $0 <machine-name>" >&2
    echo "Example: $0 exeprox-lax-na-01" >&2
    exit 1
fi

INSTANCE_NAME="$1"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

ARCH=amd64

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "==========================================="
echo "Deploying exeprox to NetActuate: ${INSTANCE_NAME}"
echo "==========================================="
echo ""

# Check Tailscale connectivity
echo "Checking Tailscale connection to VM..."
TAILSCALE_HOST="ubuntu@$INSTANCE_NAME"

if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$TAILSCALE_HOST" "echo 'Tailscale SSH connection successful'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot SSH to ${INSTANCE_NAME} via Tailscale${NC}"
    exit 1
fi

echo -e "${GREEN}Tailscale SSH access verified${NC}"
echo ""

cd "$REPO_ROOT"
go mod verify

# Generate timestamp for this deployment
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BINARY_NAME="exeprox.$TIMESTAMP"

echo -e "${YELLOW}Building binary...${NC}"
echo "Binary name: $BINARY_NAME"

GOOS=linux GOARCH=${ARCH} CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$BINARY_NAME" ./cmd/exeprox

if [ ! -f "/tmp/$BINARY_NAME" ]; then
    echo -e "${RED}ERROR: Failed to build binary${NC}"
    exit 1
fi

BINARY_SIZE=$(ls -lh "/tmp/$BINARY_NAME" | awk '{print $5}')
echo -e "${GREEN}Binary built successfully (size: $BINARY_SIZE)${NC}"
echo ""

# Deploy to VM
echo -e "${YELLOW}Deploying to VM...${NC}"

echo "Copying binary to VM..."
if ! scp "/tmp/$BINARY_NAME" "$TAILSCALE_HOST:~/"; then
    echo -e "${RED}ERROR: Failed to copy binary to VM${NC}"
    exit 1
fi
echo -e "${GREEN}Binary uploaded${NC}"

echo "Copying systemd service file..."
if ! scp "${SCRIPT_DIR}/exeprox-prod.service" "$TAILSCALE_HOST:~/exeprox.service"; then
    echo -e "${RED}ERROR: Failed to copy service file to VM${NC}"
    exit 1
fi
echo -e "${GREEN}Service file uploaded${NC}"

# Configure binary on VM
ssh -o StrictHostKeyChecking=no "$TAILSCALE_HOST" <<EOF
chmod +x ~/$BINARY_NAME

# Create a symlink to the latest version
rm -f ~/exeprox.latest
ln -sf ~/$BINARY_NAME ~/exeprox.latest

# Create empty EnvironmentFile if it doesn't exist (service file references it)
sudo touch /etc/default/exeprox

# Install systemd service file
sudo mv ~/exeprox.service /etc/systemd/system/exeprox.service
sudo systemctl daemon-reload

echo ""
echo "Deployed versions:"
ls -la ~/exeprox.* | tail -5
EOF

# Rollback instructions
echo ""
echo -e "${YELLOW}==========================================="
echo "Rollback Instructions"
echo "==========================================="
echo -e "${NC}"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo "  ls -la ~/exeprox.*  # list all versions"
echo "  sudo ln -sf ~/exeprox.TIMESTAMP ~/exeprox.latest"
echo "  sudo systemctl restart exeprox"
echo ""

# Restart the service
ssh -o StrictHostKeyChecking=no "$TAILSCALE_HOST" <<EOF
echo "Restarting exeprox service..."
sudo systemctl enable exeprox
sudo systemctl restart exeprox

sleep 3

if sudo systemctl is-active --quiet exeprox; then
    echo "Service started successfully"
else
    echo "Service may have issues, checking logs..."
    sudo journalctl -u exeprox -n 20 --no-pager
fi

echo ""
echo "Recent service logs:"
sudo journalctl -u exeprox -n 5 --no-pager -o cat
EOF

echo ""
echo -e "${GREEN}==========================================="
echo "Deployment Complete!"
echo "==========================================="
echo -e "${NC}"
echo "Deployed version: $BINARY_NAME"
echo ""
echo "Admin access:"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo ""
echo "View logs:"
echo "  ssh ubuntu@$INSTANCE_NAME journalctl -fu exeprox"

rm -f "/tmp/$BINARY_NAME"
