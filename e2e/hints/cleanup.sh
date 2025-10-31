#!/bin/bash
# Clean up testing environment

echo "Cleaning up testing environment..."

# Kill tmux session
if tmux has-session -t testing 2>/dev/null; then
    echo "Killing tmux session..."
    tmux kill-session -t testing
fi

# Stop any containers that were created during testing
echo "Stopping test containers..."
ssh colima-exe-ctr sudo nerdctl --namespace=exe ps --filter "name=exe-testtesttestteam-" --format "{{.ID}}" | xargs -r ssh colima-exe-ctr sudo nerdctl stop
ssh colima-exe-ctr sudo nerdctl --namespace=exe ps -a --filter "name=exe-testtesttestteam-" --format "{{.ID}}" | xargs -r ssh colima-exe-ctr sudo nerdctl rm

# Clean up SSH keys if desired
read -p "Remove generated SSH keys? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    rm -f ~/.ssh/id_rsa ~/.ssh/id_rsa.pub
    echo "SSH keys removed"
fi

# Clean up temporary files
rm -f /tmp/testfile*.txt
rm -f exed.log

echo "Cleanup complete!"
