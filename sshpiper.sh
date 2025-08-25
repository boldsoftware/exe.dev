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

# Get private key from database
PRIVATE_KEY=$(sqlite3 exe.db "SELECT private_key FROM ssh_host_key WHERE id = 1;")
[ -z "$PRIVATE_KEY" ] && { echo "No SSH host key found"; exit 1; }

# Wait until something is listening on the piper plugin port
echo "Waiting for service on port $PIPER_PLUGIN_PORT..."
while ! timeout 1 bash -c "</dev/tcp/localhost/$PIPER_PLUGIN_PORT" 2>/dev/null; do
    sleep 0.1
done
echo "Port $PIPER_PLUGIN_PORT is ready"

# Start sshpiper
exec ./sshpiper/sshpiperd \
    --log-level=DEBUG \
    --drop-hostkeys-message \
    --port=2222 \
    --address=0.0.0.0 \
    --server-key-data="$(echo "$PRIVATE_KEY" | base64 -w 0)" \
    grpc --endpoint=localhost:$PIPER_PLUGIN_PORT --insecure \
    -- ./sshpiper/metrics --collect-pipe-create-errors \
    --collect-upstream-auth-failures --port 8888
