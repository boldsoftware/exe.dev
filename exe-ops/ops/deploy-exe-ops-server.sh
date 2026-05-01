#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
    echo "Usage: $0 <target-host> <environment> [addr]"
    echo "  <environment>  Environment label displayed in the UI (e.g. 'staging', 'prod')."
    echo "                 Written to a systemd drop-in as EXE_OPS_ENVIRONMENT=<environment>."
    echo "  [addr]         Listen address. Default 'tailscale:443' binds the host's tailnet IPs only,"
    echo "                 keeping the port off the public internet. Pass ':443' to bind all interfaces"
    echo "                 or ':5555' (or similar) for a non-privileged port."
    exit 1
fi

TARGET="$1"
ENVIRONMENT="$2"
ADDR="${3:-tailscale:443}"

case "$ENVIRONMENT" in
*[!a-zA-Z0-9_:-]* | "")
    echo "ERROR: invalid environment '$ENVIRONMENT' (allowed: alphanumerics, '_', '-', ':')"
    exit 1
    ;;
esac

case "$ADDR" in
*[!a-zA-Z0-9_.:-]* | "")
    echo "ERROR: invalid addr '$ADDR'"
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="exe-ops-server"
REMOTE_BIN_DIR="/opt/exe-ops/bin"
SERVICE_FILE="exe-ops-server.service"
DROPIN_DIR="/etc/systemd/system/${SERVICE_FILE}.d"
DROPIN_FILE="environment.conf"

cd "$PROJECT_DIR"
VERSION="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
BUILD_DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
LDFLAGS="-X exe.dev/exe-ops/version.Version=${VERSION} -X exe.dev/exe-ops/version.Commit=${COMMIT} -X exe.dev/exe-ops/version.Date=${BUILD_DATE}"

echo "Building UI..."
make build-ui

echo "Building $BINARY..."
GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$BINARY" ./cmd/exe-ops-server

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
# Match Manager.upload's naming so `ls /opt/exe-ops/bin/` is a usable
# self-deploy history: <binary>.<ts>-<sha[:12]>.
SHA12="$(echo "$COMMIT" | head -c 12)"
if [ -z "$SHA12" ] || [ "$SHA12" = "unknown" ]; then
    SHA12="nosha-------"
fi
VERSIONED_BINARY="${BINARY}.${TIMESTAMP}-${SHA12}"

LOCAL_SHA=$(sha256sum "$BINARY" | awk '{print $1}')
echo "Local binary sha256: $LOCAL_SHA"

echo "Deploying to $TARGET..."
ssh "$TARGET" "sudo mkdir -p $REMOTE_BIN_DIR && sudo chown \$(id -u):\$(id -g) $REMOTE_BIN_DIR"
scp "$BINARY" "$TARGET:$REMOTE_BIN_DIR/$VERSIONED_BINARY"
REMOTE_SHA=$(ssh "$TARGET" "sha256sum $REMOTE_BIN_DIR/$VERSIONED_BINARY | awk '{print \$1}'")
echo "Remote binary sha256: $REMOTE_SHA"
if [ "$LOCAL_SHA" != "$REMOTE_SHA" ]; then
    echo "ERROR: sha256 mismatch after upload! Aborting."
    exit 1
fi
ssh "$TARGET" "chmod +x $REMOTE_BIN_DIR/$VERSIONED_BINARY && ln -sf $REMOTE_BIN_DIR/$VERSIONED_BINARY $REMOTE_BIN_DIR/$BINARY"
SYMLINK_SHA=$(ssh "$TARGET" "sha256sum $REMOTE_BIN_DIR/$BINARY | awk '{print \$1}'")
if [ "$LOCAL_SHA" != "$SYMLINK_SHA" ]; then
    echo "ERROR: symlink sha256 mismatch! Expected $LOCAL_SHA, got $SYMLINK_SHA"
    exit 1
fi
echo "Symlink verified OK."
scp "$SCRIPT_DIR/$SERVICE_FILE" "$TARGET:/tmp/$SERVICE_FILE"
ssh "$TARGET" "sudo mv /tmp/$SERVICE_FILE /etc/systemd/system/$SERVICE_FILE"

# Install environment file if it doesn't already exist on the target.
ssh "$TARGET" "test -f /etc/default/exe-ops-server" || {
    echo "Installing default environment file..."
    scp "$SCRIPT_DIR/exe-ops-server.env" "$TARGET:/tmp/exe-ops-server.env"
    ssh "$TARGET" "sudo mv /tmp/exe-ops-server.env /etc/default/exe-ops-server"
}

# Install/refresh the environment drop-in that pins the environment label,
# listen address, and TLS mode (Tailscale automatic HTTPS via tscert).
echo "Writing systemd drop-in: EXE_OPS_ENVIRONMENT=$ENVIRONMENT EXE_OPS_ADDR=$ADDR EXE_OPS_TLS=true"
ssh "$TARGET" "sudo mkdir -p $DROPIN_DIR && sudo tee $DROPIN_DIR/$DROPIN_FILE >/dev/null" <<EOF
[Service]
Environment=EXE_OPS_ENVIRONMENT=$ENVIRONMENT
Environment=EXE_OPS_ADDR=$ADDR
Environment=EXE_OPS_TLS=true
EOF

echo "Reloading systemd and restarting service..."
ssh "$TARGET" "sudo systemctl daemon-reload && sudo systemctl enable $SERVICE_FILE && sudo systemctl restart $SERVICE_FILE"

echo "Checking service status..."
ssh "$TARGET" "sudo systemctl status $SERVICE_FILE --no-pager"

# Prune old versioned binaries, keeping the 5 most recent. The glob
# matches both the legacy "${BINARY}-<ts>" naming and the new
# "${BINARY}.<ts>-<sha>" naming; mixed states phase out as old files age.
echo "Pruning old binaries..."
ssh "$TARGET" "ls -1t $REMOTE_BIN_DIR/${BINARY}[.-]* 2>/dev/null | tail -n +6 | xargs -r rm -f"

rm -f "$PROJECT_DIR/$BINARY"
echo "Done."
