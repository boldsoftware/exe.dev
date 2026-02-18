#!/bin/bash
set -euo pipefail

# Get SSH host keys from database
PRIVATE_KEY=$(sqlite3 /home/ubuntu/exe.db "SELECT private_key FROM ssh_host_key WHERE id = 1;")
HOST_CERT_SIG=$(sqlite3 /home/ubuntu/exe.db "SELECT cert_sig FROM ssh_host_key WHERE id = 1;")

# Check that we got a private key
if [ -z "$PRIVATE_KEY" ]; then
    echo "ERROR: No SSH host key found in database"
    exit 1
fi

# Base64 encode the private key
PRIVATE_KEY_B64=$(printf '%s' "$PRIVATE_KEY" | base64 -w 0)

# Get tailscale IP (required for plugin endpoint and metrics)
TS_IP=$(tailscale ip -4 2>/dev/null | sed '/^[[:space:]]*$/d' | head -n1)
if [ -z "$TS_IP" ]; then
    echo "ERROR: tailscale IPv4 address required"
    exit 1
fi

# Build sshpiperd arguments
ARGS=(
    /home/ubuntu/sshpiperd.latest
    --log-level=INFO
    --port=22
    --drop-hostkeys-message
    --server-key-data="$PRIVATE_KEY_B64"
)

# Add certificate if present
if [ -n "$HOST_CERT_SIG" ]; then
    HOST_CERT_SIG_B64=$(printf '%s' "$HOST_CERT_SIG" | base64 -w 0)
    ARGS+=(--server-cert-data="$HOST_CERT_SIG_B64")
fi

# Add grpc plugin configuration — connect to exed's piper plugin over tailscale
ARGS+=(grpc --endpoint="$TS_IP:2224" --insecure)

# Add metrics plugin
ARGS+=(
    --
    /home/ubuntu/metrics.latest
    --collect-upstream-auth-failures
    --address "$TS_IP"
    --port 30303
)

# Execute sshpiperd
exec "${ARGS[@]}"
