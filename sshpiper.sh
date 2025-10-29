#!/bin/bash
set -e

# Get the piper plugin port from command-line argument, default to 2224
PIPER_PLUGIN_PORT="${1:-2224}"

# Check if timeout command exists
if ! command -v timeout &> /dev/null; then
    echo "Error: 'timeout' command not found. On macOS, run 'brew install coreutils'"
    exit 1
fi

# Build sshpiperd if needed
if [ ! -f sshpiper/sshpiperd ]; then
    cd sshpiper && go build -o sshpiperd ./cmd/sshpiperd && cd ..
fi

# Build sshpiperd if needed
if [ ! -f sshpiper/metrics ]; then
    cd sshpiper && go build -o metrics ./plugin/metrics && cd ..
fi

# Get private key (and optional cert) from database
HOST_PRIVATE_KEY=$(sqlite3 exe.db "SELECT private_key FROM ssh_host_key WHERE id = 1;")
[ -z "$HOST_PRIVATE_KEY" ] && { echo "No SSH host key found"; exit 1; }
HOST_CERT_SIG=$(sqlite3 exe.db "SELECT cert_sig FROM ssh_host_key WHERE id = 1;")

# Wait until something is listening on the piper plugin port
echo "Waiting for service on port $PIPER_PLUGIN_PORT..."
while ! timeout 1 bash -c "</dev/tcp/localhost/$PIPER_PLUGIN_PORT" 2>/dev/null; do
    sleep 0.1
done
echo "Port $PIPER_PLUGIN_PORT is ready"

METRICS_ARGS=()
if command -v tailscale &> /dev/null; then
    TS_IP_OUTPUT=$(tailscale ip -4 2>/dev/null || true)
    TS_IP_OUTPUT=$(echo "$TS_IP_OUTPUT" | sed '/^[[:space:]]*$/d')
    if [ -z "$TS_IP_OUTPUT" ]; then
        echo "Error: No Tailscale IP address found. Is Tailscale running and logged in?"
    elif [ "$(echo "$TS_IP_OUTPUT" | wc -l)" -ne 1 ]; then
        echo "Error: Multiple Tailscale IP addresses found. Please ensure only one is active."
        echo "$TS_IP_OUTPUT"
    else
        TS_IP=$TS_IP_OUTPUT
        echo "Using Tailscale IP: $TS_IP"
        METRICS_ARGS=(-- ./sshpiper/metrics --collect-pipe-create-errors --collect-upstream-auth-failures --address "$TS_IP" --port 30303)
    fi
else
    echo "'tailscale' command not found, skipping metrics plugin"
fi

# Start sshpiper
cd ./sshpiper && go build -o sshpiperd ./cmd/sshpiperd; cd ..

HOST_CERT_ARGS=()
if [ -n "$HOST_CERT_SIG" ]; then
    HOST_CERT_ARGS+=(--server-cert-data="$(printf '%s' "$HOST_CERT_SIG" | base64 -w 0)")
fi

exec ./sshpiper/sshpiperd \
    --log-level=DEBUG \
    --drop-hostkeys-message \
    --port=2222 \
    --address=0.0.0.0 \
    --server-key-data="$(printf '%s' "$HOST_PRIVATE_KEY" | base64 -w 0)" \
    "${HOST_CERT_ARGS[@]}" \
    grpc --endpoint=localhost:$PIPER_PLUGIN_PORT --insecure \
    "${METRICS_ARGS[@]}"
