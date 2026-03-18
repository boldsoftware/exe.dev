#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 3 ]; then
    echo "Usage: $0 <target-host> <server-url> <agent-token>"
    exit 1
fi

TARGET="$1"
SERVER_URL="$2"
AGENT_TOKEN="$3"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="exe-ops-agent"
REMOTE_BIN_DIR="/opt/exe-ops/bin"
SERVICE_FILE="exe-ops-agent.service"
ENV_FILE="exe-ops-agent"

echo "Building $BINARY..."
cd "$PROJECT_DIR"
VERSION="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
BUILD_DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
LDFLAGS="-X exe.dev/exe-ops/version.Version=${VERSION} -X exe.dev/exe-ops/version.Commit=${COMMIT} -X exe.dev/exe-ops/version.Date=${BUILD_DATE}"
GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$BINARY" ./cmd/exe-ops-agent

echo "Deploying to $TARGET..."
ssh "$TARGET" "sudo mkdir -p $REMOTE_BIN_DIR && sudo chown \$(id -u):\$(id -g) $REMOTE_BIN_DIR"
ssh "$TARGET" "mv -f $REMOTE_BIN_DIR/$BINARY $REMOTE_BIN_DIR/$BINARY.old 2>/dev/null || true"
scp "$BINARY" "$TARGET:$REMOTE_BIN_DIR/$BINARY"
ssh "$TARGET" "chmod +x $REMOTE_BIN_DIR/$BINARY"
scp "$SCRIPT_DIR/$SERVICE_FILE" "$TARGET:/tmp/$SERVICE_FILE"
ssh "$TARGET" "sudo mv /tmp/$SERVICE_FILE /etc/systemd/system/$SERVICE_FILE"

echo "Configuring environment..."
ssh "$TARGET" "printf 'EXE_OPS_SERVER=%s\nEXE_OPS_TOKEN=%s\n' '$SERVER_URL' '$AGENT_TOKEN' | sudo tee /etc/default/$ENV_FILE > /dev/null"

echo "Reloading systemd and restarting service..."
ssh "$TARGET" "sudo systemctl daemon-reload && sudo systemctl enable $SERVICE_FILE && sudo systemctl restart $SERVICE_FILE"

echo "Checking service status..."
ssh "$TARGET" "sudo systemctl status $SERVICE_FILE --no-pager"

ssh "$TARGET" "rm -f $REMOTE_BIN_DIR/$BINARY.old"
rm -f "$PROJECT_DIR/$BINARY"
echo "Done."
