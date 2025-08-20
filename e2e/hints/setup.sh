#!/bin/bash
# Quick setup script for exed testing

set -e

echo "Setting up exed testing environment..."

# Verify docker is working (should already be running)
if ! docker ps >/dev/null 2>&1; then
    echo "ERROR: Docker is not running or accessible."
    echo "Docker should already be running in this environment."
    echo "Do NOT start dockerd manually - this can cause conflicts."
    exit 1
fi
echo "Docker is running and accessible."

# Create tmux session with windows
echo "Creating tmux session with windows..."
tmux new-session -d -s testing
tmux new-window -t testing -n exed
tmux new-window -t testing -n sshpiper  
tmux new-window -t testing -n client

echo "Building exed..."
cd /app
make build

echo "Building sshpiper..."
cd sshpiper
go build -o sshpiperd ./cmd/sshpiperd
go build -o metrics ./plugin/metrics
cd ..

echo "Generating SSH key..."
ssh-keygen -t rsa -b 2048 -f ~/.ssh/id_rsa -N "" -q

echo "\nSetup complete! Next steps:"
echo "1. Start services: ./e2e/hints/start-services.sh"
echo "2. Begin testing: tmux send-keys -t testing:client 'ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 localhost' C-m"
echo "\nTo monitor: tmux attach -t testing"
