#!/bin/bash
set -e

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

# Wait until something is listening on port 2224
echo "Waiting for service on port 2224..."
while ! timeout 1 bash -c '</dev/tcp/localhost/2224' 2>/dev/null; do
    sleep 0.1
done
echo "Port 2224 is ready"

# Start sshpiper
exec ./sshpiper/sshpiperd \
    --log-level=DEBUG \
    --drop-hostkeys-message \
    --port=2222 \
    --address=0.0.0.0 \
    --server-key-data="$(echo "$PRIVATE_KEY" | base64 -w 0)" \
    grpc --endpoint=localhost:2224 --insecure \
    -- ./sshpiper/metrics --collect-pipe-create-errors \
    --collect-upstream-auth-failures --port 8888
