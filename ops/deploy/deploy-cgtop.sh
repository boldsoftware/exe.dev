#!/bin/bash
# Deploy cgtop to a single host.
# Builds the binary locally for linux/amd64, copies it and the systemd service
# file, and restarts the service.
#
# Usage: deploy-cgtop.sh <machine-name> [-f]

set -e

if [ $# -lt 1 ] || [ -z "$1" ] || [[ "$1" == -* ]]; then
    echo "ERROR: Machine name must be specified" >&2
    echo "Usage: $0 <machine-name> [-f]" >&2
    echo "Example: $0 exe-ctr-03" >&2
    exit 1
fi

INSTANCE_NAME="$1"
shift

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Require Slack bot token
if [ -z "$EXE_SLACK_BOT_TOKEN" ]; then
    echo "ERROR: EXE_SLACK_BOT_TOKEN is not set. Deployments require Slack notifications." >&2
    exit 1
fi

# Safety checks
"$REPO_ROOT/scripts/check-deploy-safety.sh" "$@"
"$REPO_ROOT/scripts/check-prodlock.sh" prod
"$REPO_ROOT/scripts/check-remote-sha.sh" "http://${INSTANCE_NAME}.crocodile-vector.ts.net:9090/debug/gitsha"

# Slack notification
DEPLOY_TS=$("$REPO_ROOT/scripts/deploy-notify.sh" start cgtop "" "$INSTANCE_NAME")
cleanup_notify() {
    if [ $? -ne 0 ] && [ -n "$DEPLOY_TS" ]; then
        "$REPO_ROOT/scripts/deploy-notify.sh" fail "$DEPLOY_TS"
    fi
}
trap cleanup_notify EXIT

ARCH=amd64

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "==========================================="
echo "Deploying cgtop to $INSTANCE_NAME"
echo "==========================================="
echo ""

# Check Tailscale connectivity
echo "Checking Tailscale connection to VM..."
TAILSCALE_HOST="ubuntu@$INSTANCE_NAME"

if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$TAILSCALE_HOST" "echo 'ok'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot SSH to $INSTANCE_NAME via Tailscale${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Tailscale SSH access verified${NC}"
echo ""

# Build binary
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BINARY_NAME="cgtop.$TIMESTAMP"

echo -e "${YELLOW}Building cgtop...${NC}"
echo "Binary name: $BINARY_NAME"

GOOS=linux GOARCH=${ARCH} CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$BINARY_NAME" ./cmd/cgtop

if [ ! -f "/tmp/$BINARY_NAME" ]; then
    echo -e "${RED}ERROR: Failed to build binary${NC}"
    exit 1
fi

BINARY_SIZE=$(ls -lh "/tmp/$BINARY_NAME" | awk '{print $5}')
echo -e "${GREEN}✓ Binary built successfully (size: $BINARY_SIZE)${NC}"
echo ""

# Deploy
echo -e "${YELLOW}Deploying to $INSTANCE_NAME...${NC}"

echo "Copying binary to VM..."
if ! scp "/tmp/$BINARY_NAME" "$TAILSCALE_HOST:~/"; then
    echo -e "${RED}ERROR: Failed to copy binary to VM${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Binary uploaded${NC}"

echo "Copying systemd service file..."
if ! scp "$SCRIPT_DIR/cgtop.service" "$TAILSCALE_HOST:~/cgtop.service"; then
    echo -e "${RED}ERROR: Failed to copy service file to VM${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Service file uploaded${NC}"

# Install binary, create symlink, restart service
ssh -o StrictHostKeyChecking=no "$TAILSCALE_HOST" <<EOF
set -e
chmod +x ~/$BINARY_NAME

rm -f ~/cgtop.latest
ln -sf ~/$BINARY_NAME ~/cgtop.latest

sudo mv ~/cgtop.service /etc/systemd/system/cgtop.service
sudo systemctl daemon-reload
sudo systemctl enable cgtop
sudo systemctl restart cgtop

sleep 2

if sudo systemctl is-active --quiet cgtop; then
    echo "✓ Service started successfully"
else
    echo "⚠ Service may have issues, checking logs..."
    sudo journalctl -u cgtop -n 20 --no-pager
    exit 1
fi

echo ""
echo "Recent logs:"
sudo journalctl -u cgtop -n 5 --no-pager -o cat
EOF

echo -e "${GREEN}✓ Service restarted${NC}"

# Health check with retries
echo ""
echo "Running health check..."
for i in $(seq 1 10); do
    if ssh "$TAILSCALE_HOST" "curl -sf http://\$(tailscale ip -4):9090/debug/gitsha" 2>/dev/null; then
        echo ""
        echo -e "${GREEN}✓ Health check passed (attempt $i)${NC}"
        break
    fi
    if [ "$i" -eq 10 ]; then
        echo -e "${RED}ERROR: Health check timed out${NC}"
        exit 1
    fi
    sleep 2
done

# Rollback instructions
echo ""
echo -e "${YELLOW}==========================================="
echo "Rollback Instructions"
echo "==========================================="
echo -e "${NC}"
echo "  ssh ubuntu@$INSTANCE_NAME"
echo "  ls -la ~/cgtop.*       # list all versions"
echo "  ln -sf ~/cgtop.TIMESTAMP ~/cgtop.latest"
echo "  sudo systemctl restart cgtop"
echo ""

echo -e "${GREEN}==========================================="
echo "Deployment Complete!"
echo "==========================================="
echo -e "${NC}"
echo "Deployed version: $BINARY_NAME"
echo "View logs: ssh ubuntu@$INSTANCE_NAME journalctl -fu cgtop"

# Mark deployment as successful
"$REPO_ROOT/scripts/deploy-notify.sh" complete "$DEPLOY_TS"

rm -f "/tmp/$BINARY_NAME"
