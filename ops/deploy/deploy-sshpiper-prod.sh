#!/bin/bash
# Deploy script for sshpiper binary
# Builds sshpiper locally and deploys to prod VM

set -e

INSTANCE_NAME="exed-02"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "==========================================="
echo "Deploying sshpiper to Production"
echo "==========================================="
echo ""

# Check Tailscale connectivity
echo "Checking Tailscale connection to VM..."
TAILSCALE_HOST="ubuntu@$INSTANCE_NAME"

if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$TAILSCALE_HOST" "echo 'Tailscale SSH connection successful'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot SSH to the production VM via Tailscale${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Tailscale SSH access verified${NC}"
echo "Target VM: $INSTANCE_NAME (via Tailscale)"
echo ""

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

# Enable service (but don't start it yet as requested)
sudo systemctl enable sshpiper

# List all deployed versions
echo ""
echo "Deployed versions:"
ls -la ~/sshpiperd.* | tail -5

# Show service status
echo ""
echo "Service configuration:"
sudo systemctl status sshpiper --no-pager || true
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
echo "To start the service:"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo "  sudo systemctl start sshpiper"
echo ""
echo "To view logs:"
echo "  ssh ubuntu@$INSTANCE_NAME journalctl -fu sshpiper"
echo ""
echo "Rollback (if needed):"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo "  ls -la ~/sshpiperd.*  # list all versions"
echo "  sudo ln -sf ~/sshpiperd.TIMESTAMP ~/sshpiperd.latest"
echo "  sudo systemctl restart sshpiper"

rm -f "/tmp/$BINARY_NAME"
rm -f "/tmp/$METRICS_NAME"
