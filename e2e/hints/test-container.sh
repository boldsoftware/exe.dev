#!/bin/bash
# Test container SSH connectivity after creation

if [ $# -eq 0 ]; then
    echo "Usage: $0 <machine-name>"
    echo "Example: $0 able-yankee"
    exit 1
fi

MACHINE_NAME="$1"

echo "Testing SSH connection to container: $MACHINE_NAME"
echo "Waiting 15 seconds for SSH daemon to be ready..."
sleep 15

echo "\n=== Testing SSH connection ==="
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 "$MACHINE_NAME@localhost" 'echo "SSH connection successful"'

if [ $? -eq 0 ]; then
    echo "\n=== Testing id command ==="
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 "$MACHINE_NAME@localhost" 'id'
    
    echo "\n=== Testing hostname ==="
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 "$MACHINE_NAME@localhost" 'hostname'
    
    echo "\n=== SSH test complete ==="
else
    echo "\nSSH connection failed. Check logs:"
    echo "  tmux capture-pane -p -t testing:sshpiper"
    echo "  tmux capture-pane -p -t testing:exed"
fi
