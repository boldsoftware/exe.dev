#!/bin/bash
set -euo pipefail

echo "=== Resetting Lima hosts to initial state ==="

# Check if base instance exists
if ! limactl list | grep exe-ctr-base >/dev/null 2>&1; then
	echo "Error: Base instance exe-ctr-base not found"
	echo "Please run ./ops/setup-lima-hosts.sh first to create the base instance"
	exit 1
fi

echo "Stopping and removing current instances..."
limactl stop --tty=false exe-ctr-base -f 2>/dev/null || true
limactl stop --tty=false exe-ctr -f 2>/dev/null || true
limactl stop --tty=false exe-ctr-tests -f 2>/dev/null || true

sleep 2

limactl delete exe-ctr --tty=false -f 2>/dev/null || true
limactl delete exe-ctr-tests --tty=false -f 2>/dev/null || true

echo "Re-cloning from base..."
limactl clone --tty=false exe-ctr-base exe-ctr
limactl clone --tty=false exe-ctr-base exe-ctr-tests

echo "Starting cloned instances..."
limactl start --tty=false exe-ctr
limactl start --tty=false exe-ctr-tests

echo ""
echo "=========================================="
echo "Lima hosts restored to initial state"
echo "=========================================="
echo ""
