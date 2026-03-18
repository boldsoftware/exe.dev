#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 3 ]; then
    echo "Usage: $0 <env-file> <server-url> <token>"
    exit 1
fi

ENV_FILE="$1"
SERVER_URL="$2"
TOKEN="$3"

if [ ! -f "$ENV_FILE" ]; then
    echo "Error: env file not found: $ENV_FILE"
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="exe-ops-agent"
REMOTE_BIN_DIR="/opt/exe-ops/bin"
SERVICE_FILE="exe-ops-agent.service"

# shellcheck source=/dev/null
source "$ENV_FILE"

echo "Server URL: $SERVER_URL"
echo ""

echo "Building $BINARY..."
cd "$PROJECT_DIR"
GOOS=linux GOARCH=amd64 go build -o "$BINARY" ./cmd/exe-ops-agent

deploy_agent() {
    local vm_name="$1"
    local ip="$2"
    local server_type="$3"

    echo ""
    echo "=== Deploying to $vm_name ($ip) [${server_type}] ==="

    ssh "${ip}" "sudo mkdir -p $REMOTE_BIN_DIR"

    echo "  Copying binary..."
    scp "$PROJECT_DIR/$BINARY" "${ip}:/tmp/$BINARY"
    ssh "${ip}" "sudo mv /tmp/$BINARY $REMOTE_BIN_DIR/$BINARY && sudo chmod +x $REMOTE_BIN_DIR/$BINARY"

    echo "  Installing service file..."
    scp "$SCRIPT_DIR/$SERVICE_FILE" "${ip}:/tmp/$SERVICE_FILE"
    ssh "${ip}" "sudo mv /tmp/$SERVICE_FILE /etc/systemd/system/$SERVICE_FILE"

    echo "  Installing environment file..."
    ssh "${ip}" "sudo tee /etc/default/exe-ops-agent > /dev/null" <<EOF
# /etc/default/exe-ops-agent
EXE_OPS_SERVER=${SERVER_URL}
EXE_OPS_TOKEN=${TOKEN}
EXE_OPS_NAME=${vm_name}
EOF

    echo "  Enabling and restarting service..."
    ssh "${ip}" "sudo systemctl daemon-reload && sudo systemctl enable $SERVICE_FILE && sudo systemctl restart $SERVICE_FILE"

    echo "  Checking service status..."
    ssh "${ip}" "sudo systemctl status $SERVICE_FILE --no-pager" || true
}

# Deploy to exeprox nodes.
i=1
while true; do
    vm_var="EXEPROX_${i}_VM"
    ip_var="EXEPROX_${i}_IP"
    vm_name="${!vm_var:-}"
    ip="${!ip_var:-}"
    [ -z "$vm_name" ] && break

    deploy_agent "$vm_name" "$ip" "exeprox"
    i=$((i + 1))
done

# Deploy to exelet nodes.
i=1
while true; do
    vm_var="EXELET_${i}_VM"
    ip_var="EXELET_${i}_IP"
    vm_name="${!vm_var:-}"
    ip="${!ip_var:-}"
    [ -z "$vm_name" ] && break

    deploy_agent "$vm_name" "$ip" "exelet"
    i=$((i + 1))
done

rm -f "$PROJECT_DIR/$BINARY"
echo ""
echo "Done. Deployed to all exeprox and exelet nodes."
