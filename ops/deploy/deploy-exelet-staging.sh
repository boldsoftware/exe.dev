#!/bin/bash
# Deploy script for exelet binary
# Builds the binary locally and deploys to staging VM

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Slack notification (best-effort)
DEPLOY_TS=$("$REPO_ROOT/scripts/deploy-notify.sh" start exelet-staging)
cleanup_notify() {
    if [ $? -ne 0 ] && [ -n "$DEPLOY_TS" ]; then
        "$REPO_ROOT/scripts/deploy-notify.sh" fail "$DEPLOY_TS"
    fi
}
trap cleanup_notify EXIT

# staging / prod machines are intel only for now
ARCH=amd64

INSTANCE_NAME="exe-ctr-staging-01"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "==========================================="
echo "Deploying exelet to Staging"
echo "==========================================="
echo ""

# Check Tailscale connectivity
echo "Checking Tailscale connection to VM..."
TAILSCALE_HOST="ubuntu@$INSTANCE_NAME"

if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$TAILSCALE_HOST" "echo 'Tailscale SSH connection successful'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot SSH to the staging VM via Tailscale${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Tailscale SSH access verified${NC}"
echo "Target VM: $INSTANCE_NAME (via Tailscale)"
echo ""

# Generate timestamp for this deployment
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BINARY_NAME="exeletd.$TIMESTAMP"

echo -e "${YELLOW}Building binary...${NC}"
echo "Binary name: $BINARY_NAME"

# Clear out and fetch the proper exelet-fs
echo -e "${YELLOW}Fetching exelet-fs content for target platform...${NC}"
rm -rf exelet/fs/{kernel,rovol}
make GOARCH=${ARCH} exelet-fs

# Build the binary
GOOS=linux GOARCH=${ARCH} CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$BINARY_NAME" ./cmd/exelet

if [ ! -f "/tmp/$BINARY_NAME" ]; then
    echo -e "${RED}ERROR: Failed to build binary${NC}"
    exit 1
fi

# Get binary size
BINARY_SIZE=$(ls -lh "/tmp/$BINARY_NAME" | awk '{print $5}')
echo -e "${GREEN}✓ Binary built successfully (size: $BINARY_SIZE)${NC}"
echo ""

# Deploy to VM
echo -e "${YELLOW}Deploying to VM...${NC}"

# Copy binary to VM via Tailscale
echo "Copying binary to VM..."
if ! scp "/tmp/$BINARY_NAME" "$TAILSCALE_HOST:~/"; then
    echo -e "${RED}ERROR: Failed to copy binary to VM${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Binary uploaded${NC}"

# Copy systemd service file
echo "Copying systemd service file..."
if ! scp "ops/deploy/exelet.service" "$TAILSCALE_HOST:~/exelet.service"; then
    echo -e "${RED}ERROR: Failed to copy service file to VM${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Service file uploaded${NC}"

# Make binary executable and create symlink
echo "Configuring binary on VM..."
ssh -o StrictHostKeyChecking=no "$TAILSCALE_HOST" <<EOF
# Make binary executable
chmod +x ~/$BINARY_NAME

# Verify permissions were set correctly
if [ -x ~/$BINARY_NAME ]; then
    echo "✓ Binary permissions set correctly"
else
    echo "⚠ Warning: Binary may not be executable"
    ls -la ~/$BINARY_NAME
fi

# Create a symlink to the latest version
rm -f ~/exeletd.latest
ln -sf ~/$BINARY_NAME ~/exeletd.latest

# Install systemd service file
sudo mv ~/exelet.service /etc/systemd/system/exelet.service
sudo systemctl daemon-reload

# List all deployed versions
echo ""
echo "Deployed versions:"
ls -la ~/exeletd.* | tail -5

# Restart the service with the new binary
echo ""
echo "Restarting exelet service..."
sudo systemctl restart exelet

# Wait for service to start
sleep 3

# Check service status
if sudo systemctl is-active --quiet exelet; then
    echo "✓ Service started successfully"
else
    echo "⚠ Service may have issues, checking logs..."
    sudo journalctl -u exelet -n 20 --no-pager
fi

# Show recent logs
echo ""
echo "Recent service logs:"
sudo journalctl -u exelet -n 5 --no-pager -o cat
EOF

echo -e "${GREEN}✓ Service configuration completed${NC}"

# Clear out and fetch the proper exelet-fs
echo -e "${YELLOW}Restoring exelet-fs content for current platform...${NC}"
rm -rf exelet/fs/{kernel,rovol}
make exelet-fs

echo ""
echo -e "${GREEN}==========================================="
echo "Deployment Complete!"
echo "==========================================="
echo -e "${NC}"
echo "Deployed version: $BINARY_NAME"
echo "Timestamp: $TIMESTAMP"
echo ""
echo "Admin access:"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo ""
echo "View logs:"
echo "  ssh ubuntu@$INSTANCE_NAME journalctl -fu exelet"
echo ""
echo "Rollback (if needed):"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo "  ls -la ~/exeletd.*  # list all versions"
echo "  sudo ln -sf ~/exeletd.TIMESTAMP ~/exeletd.latest"
echo "  sudo systemctl restart exelet"

# Mark deployment as successful
"$REPO_ROOT/scripts/deploy-notify.sh" complete "$DEPLOY_TS"

rm -f "/tmp/$BINARY_NAME"
