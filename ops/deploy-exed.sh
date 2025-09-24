#!/bin/bash
# Deploy script for exed binary
# Builds the binary locally and deploys to production VM

set -e

INSTANCE_NAME="exed-01"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "==========================================="
echo "Deploying exed to Production"
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
BINARY_NAME="exed.$TIMESTAMP"

echo -e "${YELLOW}Building binary...${NC}"
echo "Binary name: $BINARY_NAME"

# Build the binary
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$BINARY_NAME" ./cmd/exed/exed.go

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

# Make binary executable and create symlink
echo "Configuring binary on VM..."
ssh -o StrictHostKeyChecking=no "$TAILSCALE_HOST" << EOF
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
rm -f ~/exed.latest
ln -sf ~/$BINARY_NAME ~/exed.latest

# List all deployed versions
echo ""
echo "Deployed versions:"
ls -la ~/exed.* | tail -5

# Restart the service with the new binary
echo ""
echo "Restarting exed service..."
sudo systemctl restart exed

# Wait for service to start
sleep 3

# Check service status
if sudo systemctl is-active --quiet exed; then
    echo "✓ Service started successfully"
else
    echo "⚠ Service may have issues, checking logs..."
    sudo journalctl -u exed -n 20 --no-pager
fi

# Show recent logs
echo ""
echo "Recent service logs:"
sudo journalctl -u exed -n 5 --no-pager -o cat
EOF

echo -e "${GREEN}✓ Service configuration completed${NC}"

echo ""
echo -e "${YELLOW}Testing service health...${NC}"
echo "Waiting for https://exe.dev/health to respond..."

# Health check loop running on dev machine
for i in {1..30}; do
    if curl -s -o /dev/null -w "" https://exe.dev/health 2>/dev/null; then
        echo -e "${GREEN}✓ Service is responding (attempt $i/30)${NC}"
        break
    fi
    if [ $i -eq 30 ]; then
        echo -e "${YELLOW}⚠ Service health check timed out after 60 seconds${NC}"
        echo "Service may still be starting - this is normal for the first restart"
    else
        sleep 2
    fi
done

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
echo "  ssh ubuntu@INSTANCE_NAME journalctl -fu exed"
echo ""
echo "Rollback (if needed):"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo "  ls -la ~/exed.*  # list all versions"
echo "  sudo ln -sf ~/exed.TIMESTAMP ~/exed.latest"
echo "  sudo systemctl restart exed"

rm -f "/tmp/$BINARY_NAME"
