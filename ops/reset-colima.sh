#!/bin/bash
# Reset colima development environment when things get stuck
# This script cleans up stuck containers, VMs, and processes

set -e

COLIMA_PROFILE="${1:-exe-ctr-colima}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SSH_PORT=22251  # Fixed port used by setup-colima-host.sh

echo "=== Resetting Colima profile: $COLIMA_PROFILE ==="
echo ""

# Function to run commands in colima
colima_exec() {
    ssh "$COLIMA_PROFILE" "$@" 2>/dev/null || true
}

colima_sudo() {
    ssh "$COLIMA_PROFILE" "sudo $*" 2>/dev/null || true
}

echo "1. Checking current state..."
echo "   Cloud-hypervisor processes: $(colima_exec 'ps aux | grep cloud-hypervisor | grep -v grep | wc -l')"
echo "   Kata VMs: $(colima_sudo 'ls /run/vc/vm 2>/dev/null | wc -l')"
echo "   Containers: $(colima_sudo 'nerdctl --namespace exe ps -a -q | wc -l')"
echo "   System load: $(colima_exec 'uptime')"
echo ""

# Kill any SSH tunnels first
echo "2. Cleaning up SSH tunnels..."
pkill -f "ssh -N.*$COLIMA_PROFILE" 2>/dev/null || true
echo "   Killed SSH tunnel processes"
echo ""

echo "3. Stopping all exe namespace containers..."
# Stop all running containers
for id in $(ssh "$COLIMA_PROFILE" "sudo nerdctl --namespace exe ps -q" 2>/dev/null); do
    [ -n "$id" ] && colima_sudo "nerdctl --namespace exe stop --time 5 $id" || true
done
echo "   Stopped containers"
echo ""

echo "4. Removing all exe namespace containers..."
# Remove all containers (including stopped ones)
for id in $(ssh "$COLIMA_PROFILE" "sudo nerdctl --namespace exe ps -a -q" 2>/dev/null); do
    [ -n "$id" ] && colima_sudo "nerdctl --namespace exe rm -f $id" || true
done
echo "   Removed containers"
echo ""

echo "5. Cleaning up stuck Kata VMs..."
# Kill cloud-hypervisor processes
colima_sudo "pkill -9 cloud-hypervisor 2>/dev/null" || true
echo "   Killed cloud-hypervisor processes"

# Clean up VM directories
colima_sudo "rm -rf /run/vc/vm/* 2>/dev/null" || true
echo "   Cleaned VM directories"

# Clean up sandbox directories
colima_sudo "rm -rf /run/vc/sbs/* 2>/dev/null" || true
echo "   Cleaned sandbox directories"
echo ""

echo "6. Restarting containerd..."
colima_sudo "systemctl restart containerd"
sleep 3
echo "   Containerd restarted"
echo ""

echo "7. Cleaning up networks..."
# Remove exe-alloc networks
for id in $(ssh "$COLIMA_PROFILE" "sudo nerdctl --namespace exe network ls -q" 2>/dev/null | grep '^exe-alloc'); do
    [ -n "$id" ] && colima_sudo "nerdctl --namespace exe network rm $id" || true
done
echo "   Cleaned up exe networks"
echo ""

echo "8. Final state check..."
echo "   Cloud-hypervisor processes: $(colima_exec 'ps aux | grep cloud-hypervisor | grep -v grep | wc -l')"
echo "   Kata VMs: $(colima_sudo 'ls /run/vc/vm 2>/dev/null | wc -l')"
echo "   Containers: $(colima_sudo 'nerdctl --namespace exe ps -a -q | wc -l')"
echo "   System load: $(colima_exec 'uptime')"
echo ""

# Check if VM is still running
if colima status -p ${COLIMA_PROFILE} 2>/dev/null | grep -q "Running"; then
    echo "9. VM is running with fixed SSH port ${SSH_PORT}"
    echo "   Testing SSH connection..."
    if ssh -o ConnectTimeout=3 exe-ctr-colima "echo '   SSH working'" 2>/dev/null; then
        echo "   ✓ SSH connection verified"
    else
        echo "   ⚠️ SSH not working, you may need to check ~/.ssh/config"
    fi
else
    echo "9. VM is not running. Starting it with fixed port..."
    colima start -p ${COLIMA_PROFILE} --ssh-port ${SSH_PORT}
    sleep 3
    echo "   VM started on port ${SSH_PORT}"
    
    echo "10. Testing SSH connection..."
    if ssh -o ConnectTimeout=3 exe-ctr-colima "echo '   SSH working'" 2>/dev/null; then
        echo "   ✓ SSH connection verified"
    else
        echo "   ⚠️ SSH not working, you may need to run ./ops/setup-colima-host.sh"
    fi
fi

echo ""
echo "11. Restoring containerd configuration..."
# Colima overwrites containerd config on restart, so we need to restore it
if [ -f "${SCRIPT_DIR}/restore-containerd-config.sh" ]; then
    "${SCRIPT_DIR}/restore-containerd-config.sh" "${COLIMA_PROFILE}"
else
    echo "   ⚠️ Warning: restore-containerd-config.sh not found"
    echo "   Containerd may not be properly configured for nydus/kata"
fi

echo ""
echo "=== Reset complete! ==="
echo ""
echo "Tips:"
echo "- Wait a few seconds for the system to settle"
echo "- You can now restart exed with: go run ./cmd/exed -dev=local"
echo "- If issues persist, you may need to restart colima: colima restart $COLIMA_PROFILE"