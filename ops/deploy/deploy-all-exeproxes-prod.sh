#!/bin/bash
# Deploy exeprox to ALL prod machines in parallel.
# Discovers exeprox hosts via tailscale and runs
# deploy-exeprox-prod.sh for each one concurrently.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SCRIPT="$SCRIPT_DIR/deploy-exeprox-prod.sh"

# Pass through flags (e.g. -f) to each deploy invocation
FLAGS=("$@")

echo "Discovering prod exeproxes via tailscale..."
NAMES=$(tailscale status | grep exeprox | grep -v "\-staging" | awk '{print $2}')

if [ -z "$NAMES" ]; then
    echo "ERROR: No prod exeproxes found" >&2
    exit 1
fi

COUNT=$(echo "$NAMES" | wc -l | tr -d ' ')
echo "Found $COUNT prod exeproxes:"
echo "$NAMES" | sed 's/^/  /'
echo ""

LOGDIR=$(mktemp -d)
PIDS=()
MACHINES=()

for name in $NAMES; do
    echo "Starting deploy: $name"
    "$DEPLOY_SCRIPT" "$name" "${FLAGS[@]}" >"$LOGDIR/$name.log" 2>&1 &
    PIDS+=($!)
    MACHINES+=("$name")
done

echo ""
echo "All $COUNT deploys launched. Waiting for completion..."
echo ""

FAILED=0
for i in "${!PIDS[@]}"; do
    pid=${PIDS[$i]}
    machine=${MACHINES[$i]}
    if wait "$pid"; then
        echo "✓ $machine deployed successfully"
    else
        echo "✗ $machine FAILED (see $LOGDIR/$machine.log)"
        FAILED=$((FAILED + 1))
    fi
done

echo ""
if [ "$FAILED" -eq 0 ]; then
    echo "All $COUNT exeproxes deployed successfully."
    rm -rf "$LOGDIR"
else
    echo "$FAILED/$COUNT deploys failed. Logs in $LOGDIR"
    exit 1
fi
