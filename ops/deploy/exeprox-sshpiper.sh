#!/bin/bash
set -euo pipefail

# Get SSH host keys from files (placed during setup)
PRIVATE_KEY=$(cat /home/ubuntu/host_private_key)
HOST_CERT_SIG=$(cat /home/ubuntu/host_cert_sig 2>/dev/null || true)

# Check that we got a private key
if [ -z "$PRIVATE_KEY" ]; then
    echo "ERROR: No SSH host key found in /home/ubuntu/host_private_key"
    exit 1
fi

# Base64 encode the private key
PRIVATE_KEY_B64=$(printf '%s' "$PRIVATE_KEY" | base64 -w 0)

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

# Add grpc plugin configuration - connect to exed-02
ARGS+=(grpc --endpoint=exed-02:2224 --insecure)

# Add metrics plugin if tailscale is available
if command -v tailscale >/dev/null 2>&1; then
    TS_IP=$(tailscale ip -4 2>/dev/null | sed '/^[[:space:]]*$/d' | head -n1)
    if [ -z "$TS_IP" ]; then
        echo "ERROR: tailscale IPv4 address required"
        exit 1
    fi
    ARGS+=(
        --
        /home/ubuntu/metrics.latest
        --collect-upstream-auth-failures
        --address "$TS_IP"
        --port 30303
    )
fi

# Execute sshpiperd
exec "${ARGS[@]}"
