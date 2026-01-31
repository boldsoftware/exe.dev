#!/bin/bash
# Deploy maddy mail server to production
# One-time setup - see devdocs/inbound_email_deployment.md for prerequisites

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

INSTANCE_NAME="exed-02"
BOX_DOMAIN="exe.xyz"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "==========================================="
echo "Deploying maddy to Prod"
echo "==========================================="
echo ""

# Check Tailscale connectivity
echo "Checking Tailscale connection to VM..."
TAILSCALE_HOST="ubuntu@$INSTANCE_NAME"

if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$TAILSCALE_HOST" "echo 'SSH connection successful'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot SSH to the prod VM via Tailscale${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Tailscale SSH access verified${NC}"
echo ""

# Create temporary config with substituted values
TMP_CONF=$(mktemp)
sed -e "s/{BOX_DOMAIN}/$BOX_DOMAIN/g" \
    "$REPO_ROOT/ops/maddy/maddy.conf" >"$TMP_CONF"

echo -e "${YELLOW}Uploading configuration files...${NC}"

# Upload config
scp "$TMP_CONF" "$TAILSCALE_HOST:~/maddy.conf"
scp "$REPO_ROOT/ops/maddy/maddy.service" "$TAILSCALE_HOST:~/maddy.service"

rm -f "$TMP_CONF"

echo -e "${GREEN}✓ Files uploaded${NC}"

# Install and configure on remote
echo -e "${YELLOW}Installing maddy...${NC}"
ssh -o StrictHostKeyChecking=no "$TAILSCALE_HOST" <<'EOF'
set -e

# Pin maddy version
MADDY_VERSION="0.8.2"

# Install maddy if not present or wrong version
current_version=$(maddy version 2>/dev/null | head -1 | grep -oP 'v\K[0-9.]+' || echo "none")
if [ "$current_version" != "$MADDY_VERSION" ]; then
    echo "Installing maddy $MADDY_VERSION from GitHub..."

    # Install zstd if not present
    if ! command -v zstd &>/dev/null; then
        sudo apt-get update
        sudo apt-get install -y zstd
    fi

    # Download and extract binary from GitHub releases
    TARBALL="maddy-${MADDY_VERSION}-x86_64-linux-musl.tar.zst"
    DOWNLOAD_URL="https://github.com/foxcpp/maddy/releases/download/v${MADDY_VERSION}/${TARBALL}"

    cd /tmp
    curl -fsSL -o "$TARBALL" "$DOWNLOAD_URL"

    # Extract (creates maddy-${VERSION}-x86_64-linux-musl directory)
    tar --zstd -xf "$TARBALL"

    # Install binaries
    sudo install -m 755 "maddy-${MADDY_VERSION}-x86_64-linux-musl/maddy" /usr/bin/maddy

    # Clean up
    rm -rf "$TARBALL" "maddy-${MADDY_VERSION}-x86_64-linux-musl"

    # Verify installation
    if ! /usr/bin/maddy version &>/dev/null; then
        echo "ERROR: maddy binary installation failed"
        exit 1
    fi
    echo "maddy installed successfully: $(/usr/bin/maddy version 2>&1 | head -1)"
fi

# Create maddy user if not exists
if ! id maddy &>/dev/null; then
    sudo useradd -r -s /sbin/nologin -d /var/lib/maddy maddy
fi

# Create directories
sudo mkdir -p /var/lib/maddy /run/maddy /etc/maddy
sudo chown maddy:maddy /var/lib/maddy /run/maddy

# Install configuration
sudo mv ~/maddy.conf /etc/maddy/maddy.conf
sudo chown root:root /etc/maddy/maddy.conf
sudo chmod 644 /etc/maddy/maddy.conf

# Create environment file for prod
cat <<ENVFILE | sudo tee /etc/default/maddy
BOX_DOMAIN=exe.xyz
ENVFILE

# Install service file
sudo mv ~/maddy.service /etc/systemd/system/maddy.service
sudo systemctl daemon-reload

# Ensure maddy can access the LMTP socket directory and certs
sudo usermod -a -G ubuntu maddy 2>/dev/null || true

# Fix cert permissions so maddy (in ubuntu group) can read them
sudo chmod 750 /home/ubuntu/certs
sudo chmod 640 /home/ubuntu/certs/*

echo "✓ maddy installed and configured"

# Enable and start service
sudo systemctl enable maddy
sudo systemctl restart maddy

sleep 2

if sudo systemctl is-active --quiet maddy; then
    echo "✓ maddy service started successfully"
else
    echo "⚠ maddy service may have issues, checking logs..."
    sudo journalctl -u maddy -n 20 --no-pager
fi
EOF

echo ""
echo -e "${GREEN}==========================================="
echo "Deployment Complete!"
echo "==========================================="
echo -e "${NC}"
echo "Box domain: $BOX_DOMAIN"
echo "Mail domain: mail.$BOX_DOMAIN"
echo ""
echo "View logs:"
echo "  ssh ubuntu@$INSTANCE_NAME journalctl -fu maddy"
echo ""
echo "Test with:"
echo "  swaks --to test@vmname.$BOX_DOMAIN --server mail.$BOX_DOMAIN"
