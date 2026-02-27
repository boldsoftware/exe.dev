#!/bin/bash
# Deploy script for exelet binary to a standalone SSH host.
# Builds the binary locally and deploys to target host.
#
# Usage: ./deploy-exelet-standalone.sh <ssh-host>
#
# Examples:
#   ./deploy-exelet-standalone.sh ubuntu@192.168.1.100
#   ./deploy-exelet-standalone.sh ubuntu@myserver.example.com
#   ./deploy-exelet-standalone.sh myserver  # uses ~/.ssh/config
#
set -euo pipefail

if [ $# -ne 1 ] || [ -z "$1" ]; then
    echo "ERROR: SSH host must be specified" >&2
    echo "Usage: $0 <ssh-host>" >&2
    echo "Example: $0 ubuntu@192.168.1.100" >&2
    exit 1
fi

SSH_HOST="$1"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Detect target architecture and user info
echo "Detecting target system info..."
REMOTE_INFO=$(ssh -o ConnectTimeout=10 -o BatchMode=yes "$SSH_HOST" 'echo "$(uname -m)|$(whoami)|$HOME"' 2>/dev/null || true)

if [ -z "$REMOTE_INFO" ]; then
    echo "ERROR: Cannot connect to $SSH_HOST" >&2
    exit 1
fi

TARGET_ARCH=$(echo "$REMOTE_INFO" | cut -d'|' -f1)
REMOTE_USER=$(echo "$REMOTE_INFO" | cut -d'|' -f2)
REMOTE_HOME=$(echo "$REMOTE_INFO" | cut -d'|' -f3)

case "$TARGET_ARCH" in
x86_64)
    GOARCH="amd64"
    ;;
aarch64)
    GOARCH="arm64"
    ;;
*)
    echo "ERROR: Unsupported architecture: $TARGET_ARCH" >&2
    exit 1
    ;;
esac

echo "Target: $SSH_HOST"
echo "  Arch: $TARGET_ARCH ($GOARCH)"
echo "  User: $REMOTE_USER"
echo "  Home: $REMOTE_HOME"
echo ""

# Generate timestamp for this deployment
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BINARY_NAME="exeletd.$TIMESTAMP"

echo "=== Building binary ==="
echo "Binary name: $BINARY_NAME"

# Fetch exelet-fs for target platform (cached per-architecture)
cd "$REPO_ROOT"
make GOARCH=${GOARCH} exelet-fs

# Build exe-init (pure Go, cross-compiles trivially)
make GOARCH=${GOARCH} exe-init

# Build the binary
GOOS=linux GOARCH=${GOARCH} CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$BINARY_NAME" ./cmd/exelet

if [ ! -f "/tmp/$BINARY_NAME" ]; then
    echo "ERROR: Failed to build binary" >&2
    exit 1
fi

BINARY_SIZE=$(ls -lh "/tmp/$BINARY_NAME" | awk '{print $5}')
echo "Binary built successfully (size: $BINARY_SIZE)"
echo ""

# Deploy to host
echo "=== Deploying to $SSH_HOST ==="

# Copy binary
echo "Copying binary..."
if ! scp "/tmp/$BINARY_NAME" "$SSH_HOST:~/"; then
    echo "ERROR: Failed to copy binary" >&2
    exit 1
fi
echo "Binary uploaded"

# Generate and copy systemd service file
echo "Generating systemd service file..."
cat >/tmp/exelet.service <<EOF
[Unit]
Description=exe.dev exelet (standalone)
After=network.target
Wants=network-online.target

[Service]
Type=simple
CPUWeight=1024
IOWeight=1024
LimitNOFILE=1048576
WorkingDirectory=/data/exelet
EnvironmentFile=-/etc/default/exelet
KillMode=process

# Use the latest exeletd - detect routable IP from default route interface
ExecStart=/bin/bash -c 'IP=\$\$(ip -4 route get 1.1.1.1 | sed -n "s/.*src \\([0-9.]*\\).*/\\1/p"); exec "${REMOTE_HOME}/exeletd.latest" "-D" "--stage=test" "--listen-address=tcp://:9080" "--http-addr=:9081" "--exed-url=http://localhost:8080"'

Restart=always
RestartSec=5

# Use journald for logging
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

if ! scp /tmp/exelet.service "$SSH_HOST:~/exelet.service"; then
    echo "ERROR: Failed to copy service file" >&2
    rm -f /tmp/exelet.service
    exit 1
fi
rm -f /tmp/exelet.service
echo "Service file uploaded"

# Configure on remote host
echo "Configuring on remote host..."
ssh "$SSH_HOST" <<EOF
set -e

# Make binary executable
chmod +x ~/$BINARY_NAME

# Create symlink to latest version
rm -f ~/exeletd.latest
ln -sf ~/$BINARY_NAME ~/exeletd.latest

# Ensure data directory exists
sudo mkdir -p /data/exelet

# Install systemd service file
sudo mv ~/exelet.service /etc/systemd/system/exelet.service
sudo systemctl daemon-reload
sudo systemctl enable exelet

# List deployed versions
echo ""
echo "Deployed versions:"
ls -la ~/exeletd.* 2>/dev/null | tail -5 || echo "  (first deployment)"

# Restart the service
echo ""
echo "Restarting exelet service..."
sudo systemctl restart exelet

# Wait for service to start
sleep 3

# Check service status
if sudo systemctl is-active --quiet exelet; then
    echo "Service started successfully"
else
    echo "Service may have issues, checking logs..."
    sudo journalctl -u exelet -n 20 --no-pager
    exit 1
fi

# Show recent logs
echo ""
echo "Recent service logs:"
sudo journalctl -u exelet -n 5 --no-pager -o cat
EOF

# Cleanup
rm -f "/tmp/$BINARY_NAME"

echo ""
echo "==========================================="
echo "Deployment Complete!"
echo "==========================================="
echo ""
echo "Deployed version: $BINARY_NAME"
echo "Timestamp: $TIMESTAMP"
echo ""
echo "View logs:"
echo "  ssh $SSH_HOST journalctl -fu exelet"
echo ""
echo "Rollback (if needed):"
echo "  ssh $SSH_HOST"
echo "  ls -la ~/exeletd.*  # list all versions"
echo "  sudo ln -sf ~/exeletd.TIMESTAMP ~/exeletd.latest"
echo "  sudo systemctl restart exelet"
