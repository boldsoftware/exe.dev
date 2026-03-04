#!/bin/bash
# Deploy exelet to ALL staging machines in parallel.
# Fetches the list of exelets from exe-staging.dev and runs
# deploy-exelet-staging.sh for each one concurrently.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SCRIPT="$SCRIPT_DIR/deploy-exelet-staging.sh"

echo "Fetching staging exelet list..."
EXELETS_JSON=$(ssh exe-staging.dev exelets --json)

NAMES=$(echo "$EXELETS_JSON" | jq -r '.exelets[].host')

if [ -z "$NAMES" ]; then
    echo "ERROR: No exelets found" >&2
    exit 1
fi

COUNT=$(echo "$NAMES" | wc -l | tr -d ' ')
echo "Found $COUNT staging exelets:"
echo "$NAMES" | sed 's/^/  /'
echo ""

LOGDIR=$(mktemp -d)
PIDS=()
MACHINES=()

for name in $NAMES; do
    echo "Starting deploy: $name"
    "$DEPLOY_SCRIPT" "$name" >"$LOGDIR/$name.log" 2>&1 &
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
    echo "All $COUNT exelets deployed successfully."
    rm -rf "$LOGDIR"
else
    echo "$FAILED/$COUNT deploys failed. Logs in $LOGDIR"
    exit 1
fi
