#!/bin/bash
# Deploy exeprox to ALL prod machines sequentially.
# Discovers exeprox hosts via tailscale and runs
# deploy-exeprox-prod.sh for each one, one at a time.

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

FAILED=0
I=0
for name in $NAMES; do
    I=$((I + 1))
    echo "=== [$I/$COUNT] Deploying $name ==="
    if "$DEPLOY_SCRIPT" "$name" ${FLAGS[@]+"${FLAGS[@]}"}; then
        echo "✓ $name deployed successfully"
    else
        echo "✗ $name FAILED"
        FAILED=$((FAILED + 1))
    fi
    echo ""
done

if [ "$FAILED" -eq 0 ]; then
    echo "All $COUNT exeproxes deployed successfully."
else
    echo "$FAILED/$COUNT deploys failed."
    exit 1
fi
