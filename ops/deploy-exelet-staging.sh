#!/bin/bash
# Deploy script for exelet binary
# Builds the binary locally and deploys to staging VM

set -e

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

echo -e "${YELLOW}Downloading binary from GitHub Actions...${NC}"
echo "Binary name: $BINARY_NAME"

# Check if gh CLI is installed
if ! command -v gh >/dev/null 2>&1; then
    echo -e "${RED}ERROR: GitHub CLI (gh) is not installed${NC}"
    echo "Install with: brew install gh (macOS) or see https://cli.github.com/"
    exit 1
fi

# Download the latest artifact from the Build Exelet Binary workflow
echo "Fetching latest exelet artifact from boldsoftware/exe..."
TEMP_DIR=$(mktemp -d)

if ! gh run download --repo boldsoftware/exe --name exeletd-amd64 --dir "$TEMP_DIR" 2>/dev/null; then
    echo -e "${RED}ERROR: Failed to download artifact from GitHub Actions${NC}"
    echo "Make sure:"
    echo "  1. You're authenticated with gh (run: gh auth login)"
    echo "  2. The 'Build Exelet Binary' workflow has run successfully"
    echo "  3. You have access to the boldsoftware/exe repository"
    rm -rf "$TEMP_DIR"
    exit 1
fi

# Move the binary to /tmp with timestamp
if [ ! -f "$TEMP_DIR/exeletd" ]; then
    echo -e "${RED}ERROR: exeletd binary not found in artifact${NC}"
    rm -rf "$TEMP_DIR"
    exit 1
fi

mv "$TEMP_DIR/exeletd" "/tmp/$BINARY_NAME"
rm -rf "$TEMP_DIR"

# Get binary size
BINARY_SIZE=$(ls -lh "/tmp/$BINARY_NAME" | awk '{print $5}')
echo -e "${GREEN}✓ Binary downloaded successfully (size: $BINARY_SIZE)${NC}"
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

rm -f "/tmp/$BINARY_NAME"
