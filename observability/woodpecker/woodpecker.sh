#!/bin/bash
set -euo pipefail
trap 'echo "Error in $0 at line $LINENO"' ERR

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKTREE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STATE_DIR="${WOODPECKER_STATE_DIR:-/home/exedev/woodpecker-state}"

echo "[woodpecker] Updating worktree to latest main..."
cd "$WORKTREE_ROOT"
git fetch origin main --quiet
git reset --hard origin/main --quiet

echo "[woodpecker] Running woodpecker from $SCRIPT_DIR"
exec python3 "$SCRIPT_DIR/woodpecker.py" --state-dir "$STATE_DIR"
