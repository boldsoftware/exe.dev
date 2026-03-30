#!/bin/bash
set -euo pipefail
trap 'echo "Error in $0 at line $LINENO"' ERR

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKTREE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STATE_DIR="${WOODPECKER_STATE_DIR:-/home/exedev/woodpecker-state}"

echo "[woodpecker] Updating worktree to latest main..."
cd "$WORKTREE_ROOT"
git fetch origin main --quiet
# Restore woodpecker files if main deleted them (commit 41910065)
if git show origin/main:observability/woodpecker/woodpecker.py &>/dev/null; then
    git reset --hard origin/main --quiet
else
    echo "[woodpecker] WARNING: woodpecker files missing from origin/main, skipping reset"
    # Still update other files from main
    git checkout origin/main -- . ':!observability/woodpecker' 2>/dev/null || true
fi

echo "[woodpecker] Running woodpecker from $SCRIPT_DIR"
exec python3 "$SCRIPT_DIR/woodpecker.py" --state-dir "$STATE_DIR"
