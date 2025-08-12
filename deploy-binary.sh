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
NC='\033[0m' # No Color

echo "==========================================="
echo "Deploying exed to Production"
echo "==========================================="
echo ""

# Get VM external IP
echo "Getting VM information..."
EXTERNAL_IP=$(gcloud compute addresses describe exed-prod-ip --region=$REGION --format="value(address)" 2>/dev/null)

if [ -z "$EXTERNAL_IP" ]; then
    echo -e "${RED}ERROR: Could not find production VM IP. Run setup-production-vm.sh first.${NC}"
    exit 1
fi

echo "Target VM: $EXTERNAL_IP"
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

# Copy binary to VM (using port 22222 for admin SSH)
echo "Copying binary to VM..."
scp -P 22222 -o StrictHostKeyChecking=no "/tmp/$BINARY_NAME" "ubuntu@$EXTERNAL_IP:~/"

if [ $? -ne 0 ]; then
    echo -e "${RED}ERROR: Failed to copy binary to VM${NC}"
    echo "Make sure you can SSH to the VM: ssh -p 22222 ubuntu@$EXTERNAL_IP"
    exit 1
fi

echo -e "${GREEN}✓ Binary uploaded${NC}"

# Make binary executable and create symlink
echo "Configuring binary on VM..."
ssh -p 22222 -o StrictHostKeyChecking=no "ubuntu@$EXTERNAL_IP" << EOF
set -e

# Make binary executable
chmod +x ~/$BINARY_NAME

# Create a symlink to the latest version
ln -sf ~/$BINARY_NAME ~/exed.latest

# List all deployed versions
echo ""
echo "Deployed versions:"
ls -la ~/exed.* | tail -5

# Get cluster credentials for the VM
gcloud container clusters get-credentials exe-cluster --zone=us-west2-a --project=$PROJECT_ID

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

# Check if service is responding
echo ""
echo "Testing service endpoints..."
curl -s -o /dev/null -w "HTTP Health Check: %{http_code}\n" http://localhost:8080/health || true

# Show recent logs
echo ""
echo "Recent service logs:"
sudo tail -n 10 /var/log/exed/exed.log 2>/dev/null || echo "No logs yet"
EOF

echo ""
echo -e "${GREEN}==========================================="
echo "Deployment Complete!"
echo "==========================================="
echo -e "${NC}"
echo "Deployed version: $BINARY_NAME"
echo "Timestamp: $TIMESTAMP"
echo ""
echo "Service endpoints:"
echo "  HTTP:  http://$EXTERNAL_IP"
echo "  HTTPS: https://$EXTERNAL_IP (if SSL configured)"
echo "  SSH:   ssh -p 22 user@$EXTERNAL_IP"
echo ""
echo "Admin access:"
echo "  ssh -p 22222 ubuntu@$EXTERNAL_IP"
echo ""
echo "View logs:"
echo "  ssh -p 22222 ubuntu@$EXTERNAL_IP 'sudo tail -f /var/log/exed/exed.log'"
echo ""
echo "Rollback (if needed):"
echo "  ssh -p 22222 ubuntu@$EXTERNAL_IP"
echo "  ls -la ~/exed.*  # list all versions"
echo "  sudo ln -sf ~/exed.TIMESTAMP ~/exed.latest"
echo "  sudo systemctl restart exed"

# Clean up temporary file
rm -f "/tmp/$BINARY_NAME"