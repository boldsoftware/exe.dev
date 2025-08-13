#!/bin/bash
# Deploy script for exed binary
# Builds the binary locally and deploys to production VM

set -e

# Configuration
PROJECT_ID="exe-dev-468515"
INSTANCE_NAME="exed-prod-01"
ZONE="us-west2-a"
REGION="us-west2"

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

# Check if gcloud is installed
if ! command -v gcloud >/dev/null 2>&1; then
    echo -e "${RED}ERROR: gcloud CLI not found${NC}"
    echo -e "${BLUE}Please install gcloud: https://cloud.google.com/sdk/docs/install${NC}"
    echo ""
    exit 1
fi

# Check if authenticated
echo "Checking gcloud authentication..."
if ! gcloud auth list --filter=status:ACTIVE --format="value(account)" 2>/dev/null | grep -q .; then
    echo -e "${RED}ERROR: Not authenticated with gcloud${NC}"
    echo -e "${BLUE}Please run: gcloud auth login${NC}"
    echo ""
    exit 1
fi

# Set the project (in case it's not set)
echo "Setting project to $PROJECT_ID..."
gcloud config set project "$PROJECT_ID" >/dev/null 2>&1

# Check Tailscale connectivity
echo "Checking Tailscale connection to VM..."
TAILSCALE_HOST="ubuntu@exed-prod-01"

if ! ssh -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o BatchMode=yes "$TAILSCALE_HOST" "echo 'Tailscale SSH connection successful'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot SSH to the production VM via Tailscale${NC}"
    echo -e "${BLUE}This could be due to:${NC}"
    echo "  1. Tailscale not running on your machine"
    echo "  2. Not connected to the same Tailscale network" 
    echo "  3. VM is not running or not connected to Tailscale"
    echo "  4. SSH key not added to the VM"
    echo ""
    echo "To fix Tailscale SSH access:"
    echo "  1. Make sure Tailscale is running: tailscale status"
    echo "  2. Test manual connection: ssh ubuntu@exed-prod-01"
    echo "  3. If that fails, check VM status in GCP Console"
    echo "  4. Verify the VM is connected: tailscale status | grep exed-prod-01"
    echo ""
    exit 1
fi

echo -e "${GREEN}✓ Tailscale SSH access verified${NC}"
echo "Target VM: exed-prod-01 (via Tailscale)"
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
if ! scp -o StrictHostKeyChecking=no "/tmp/$BINARY_NAME" "$TAILSCALE_HOST:~/"; then
    echo -e "${RED}ERROR: Failed to copy binary to VM${NC}"
    echo -e "${BLUE}Troubleshooting steps:${NC}"
    echo "  1. Test SSH connection: ssh ubuntu@exed-prod-01"
    echo "  2. Check Tailscale status: tailscale status"
    echo "  3. Verify your SSH key is loaded: ssh-add -l"
    echo "  4. Check VM status in GCP Console"
    echo ""
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
echo "Service endpoints:"
echo "  HTTP:  http://exed-prod-01 (via Tailscale)"
echo "  HTTPS: https://exed-prod-01 (if SSL configured)" 
echo "  SSH:   ssh user@exed-prod-01"
echo ""
echo "Admin access:"
echo "  ssh ubuntu@exed-prod-01"
echo ""
echo "View logs:"
echo "  ssh ubuntu@exed-prod-01 'sudo tail -f /var/log/exed/exed.log'"
echo ""
echo "Rollback (if needed):"
echo "  ssh ubuntu@exed-prod-01"
echo "  ls -la ~/exed.*  # list all versions"
echo "  sudo ln -sf ~/exed.TIMESTAMP ~/exed.latest"
echo "  sudo systemctl restart exed"

# Clean up temporary file
rm -f "/tmp/$BINARY_NAME"