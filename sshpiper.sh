#!/bin/bash
set -e

# Build sshpiperd if needed
if [ ! -f sshpiper/sshpiperd ]; then
    cd sshpiper && go build -o sshpiperd ./cmd/sshpiperd && cd ..
fi

# Get private key from database
PRIVATE_KEY=$(sqlite3 exe.db "SELECT private_key FROM ssh_host_key WHERE id = 1;")
[ -z "$PRIVATE_KEY" ] && { echo "No SSH host key found"; exit 1; }

# Start sshpiper
exec ./sshpiper/sshpiperd \
    --log-level=DEBUG \
    --port=2222 \
    --server-key-data="$(echo "$PRIVATE_KEY" | base64 -w 0)" \
    grpc --endpoint=localhost:2224 --insecure
