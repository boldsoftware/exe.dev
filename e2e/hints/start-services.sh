#!/bin/bash
# Start exed and sshpiper services in tmux windows

set -e

echo "Starting services in tmux windows..."

# Start exed
echo "Starting exed..."
tmux send-keys -t testing:exed 'cd /app && make run-dev' C-m

# Wait a moment for exed to start
sleep 3

# Start sshpiper  
echo "Starting sshpiper..."
tmux send-keys -t testing:sshpiper './sshpiper.sh' C-m

# Wait a moment for sshpiper to start
sleep 2

echo "Services started! You can now:"
echo "1. Connect to exed: tmux send-keys -t testing:client 'ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 localhost' C-m"
echo "2. Monitor services: tmux attach -t testing"
echo "3. Check logs: tmux capture-pane -p -t testing:exed or tmux capture-pane -p -t testing:sshpiper"
