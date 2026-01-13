#!/bin/bash
# Deploy script for sshpiper binary
# Builds sshpiper locally and deploys to staging VM

set -e

# Require Slack bot token for deployments
if [ -z "$EXE_SLACK_BOT_TOKEN" ]; then
    echo "ERROR: EXE_SLACK_BOT_TOKEN is not set. Deployments require Slack notifications." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Slack notification
DEPLOY_TS=$("$REPO_ROOT/scripts/deploy-notify.sh" start sshpiper-staging)
cleanup_notify() {
    if [ $? -ne 0 ] && [ -n "$DEPLOY_TS" ]; then
        "$REPO_ROOT/scripts/deploy-notify.sh" fail "$DEPLOY_TS"
    fi
}
trap cleanup_notify EXIT

INSTANCE_NAME="exed-staging-01"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "==========================================="
echo "Deploying sshpiper to Staging"
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

go mod verify
(cd deps/sshpiper && go mod verify)

# Generate timestamp for this deployment
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BINARY_NAME="sshpiperd.$TIMESTAMP"
METRICS_NAME="metrics.$TIMESTAMP"

echo -e "${YELLOW}Building sshpiper binary...${NC}"
echo "Binary name: $BINARY_NAME"

# Build sshpiper binary for linux/amd64 (same as exed deployment)
(
    cd deps/sshpiper
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$BINARY_NAME" ./cmd/sshpiperd
)

if [ ! -f "/tmp/$BINARY_NAME" ]; then
    echo -e "${RED}ERROR: Failed to build sshpiper binary${NC}"
    exit 1
fi

echo -e "${YELLOW}Building metrics binary...${NC}"
echo "Binary name: $METRICS_NAME"

# Build metrics binary for linux/amd64 (same as exed deployment)
(
    cd deps/sshpiper
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$METRICS_NAME" ./plugin/metrics
)

if [ ! -f "/tmp/$METRICS_NAME" ]; then
    echo -e "${RED}ERROR: Failed to build metrics binary${NC}"
    exit 1
fi

# Get binary size
BINARY_SIZE=$(ls -lh "/tmp/$BINARY_NAME" | awk '{print $5}')
echo -e "${GREEN}✓ sshpiper binary built successfully (size: $BINARY_SIZE)${NC}"
echo ""

# Get metrics binary size
METRICS_BINARY_SIZE=$(ls -lh "/tmp/$METRICS_NAME" | awk '{print $5}')
echo -e "${GREEN}✓ metrics binary built successfully (size: $METRICS_BINARY_SIZE)${NC}"
echo ""

# Deploy to VM
echo -e "${YELLOW}Deploying to VM...${NC}"

# Copy binaries to VM via Tailscale
echo "Copying binary to VM..."
if ! scp "/tmp/$BINARY_NAME" "$TAILSCALE_HOST:~/"; then
    echo -e "${RED}ERROR: Failed to copy binary to VM${NC}"
    exit 1
fi

if ! scp "/tmp/$METRICS_NAME" "$TAILSCALE_HOST:~/"; then
    echo -e "${RED}ERROR: Failed to copy metrics binary to VM${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Binary uploaded${NC}"

# Copy systemd service file
echo "Copying systemd service file..."
if ! scp "ops/deploy/sshpiper.service" "$TAILSCALE_HOST:/etc/systemd/system/sshpiper.service"; then
    echo -e "${RED}ERROR: Failed to copy service file to VM${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Service file uploaded${NC}"

# Copy start script
echo "Copying start script..."
if ! scp "ops/deploy/start-sshpiper.sh" "$TAILSCALE_HOST:~/start-sshpiper.sh"; then
    echo -e "${RED}ERROR: Failed to copy start script to VM${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Start script uploaded${NC}"

# Configure binary and service on VM
echo "Configuring binary and service on VM..."
ssh -o StrictHostKeyChecking=no "$TAILSCALE_HOST" <<EOF
# Make start script executable
chmod +x ~/start-sshpiper.sh

# Make binary executable
chmod +x ~/$BINARY_NAME

# Verify permissions were set correctly
if [ -x ~/$BINARY_NAME ]; then
    echo "✓ Binary permissions set correctly"
else
    echo "⚠ Warning: Binary may not be executable"
    ls -la ~/$BINARY_NAME
fi

# Make metrics plugin executable
chmod +x ~/$METRICS_NAME

if [ -x ~/$METRICS_NAME ]; then
    echo "✓ Binary permissions set correctly"
else
    echo "⚠ Warning: Binary may not be executable"
    ls -la ~/$METRICS_NAME
fi

# Create a symlinks to the latest versions
rm -f ~/sshpiperd.latest
ln -sf ~/$BINARY_NAME ~/sshpiperd.latest

rm -f ~/metrics.latest
ln -sf ~/$METRICS_NAME ~/metrics.latest

# Install systemd service file
sudo systemctl daemon-reload

# Enable and restart service
sudo systemctl enable sshpiper
echo "Restarting sshpiper service..."
sudo systemctl restart sshpiper

# Wait for service to start
sleep 2

# Check service status
if sudo systemctl is-active --quiet sshpiper; then
    echo "✓ Service restarted successfully"
else
    echo "⚠ Service may have issues, checking logs..."
    sudo journalctl -u sshpiper -n 20 --no-pager
fi

# List all deployed versions
echo ""
echo "Deployed versions:"
ls -la ~/sshpiperd.* | tail -5

# Show recent logs
echo ""
echo "Recent service logs:"
sudo journalctl -u sshpiper -n 5 --no-pager -o cat
EOF

echo -e "${GREEN}✓ Service configuration completed${NC}"

echo ""
echo -e "${GREEN}==========================================="
echo "sshpiper Deployment Complete!"
echo "==========================================="
echo -e "${NC}"
echo "Deployed version: $BINARY_NAME"
echo "Timestamp: $TIMESTAMP"
echo ""
echo "View logs:"
echo "  ssh ubuntu@$INSTANCE_NAME journalctl -fu sshpiper"
echo ""
echo "Rollback (if needed):"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo "  ls -la ~/sshpiperd.*  # list all versions"
echo "  sudo ln -sf ~/sshpiperd.TIMESTAMP ~/sshpiperd.latest"
echo "  sudo systemctl restart sshpiper"

# Mark deployment as successful
"$REPO_ROOT/scripts/deploy-notify.sh" complete "$DEPLOY_TS"

rm -f "/tmp/$BINARY_NAME"
rm -f "/tmp/$METRICS_NAME"
