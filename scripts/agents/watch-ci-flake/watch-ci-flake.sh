#!/bin/sh
set -e

REPO="boldsoftware/exe"
CLAUDE_TIMEOUT=86400  # 24h
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKDIR="$(pwd)"
STATE_DIR="${WATCH_CI_FLAKE_STATE_DIR:-$HOME/watch-ci-flake-state}"

# Autocreate state directory
mkdir -p "$STATE_DIR"

STATE_FILE="$STATE_DIR/.state"
RUNS_FILE=$(mktemp)
GH_ERR=$(mktemp)
trap 'rm -f "$RUNS_FILE" "$GH_ERR"' EXIT INT TERM

DRY_RUN=false
for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY_RUN=true ;;
    esac
done

# On first start (no state file), snapshot current failures so we only process future ones.
if [ ! -f "$STATE_FILE" ]; then
    echo "watch-ci-flake: first run, snapshotting existing failures..."
    if gh run list --repo "$REPO" --status failure \
        --json databaseId --limit 100 > "$RUNS_FILE" 2>"$GH_ERR"; then
        jq -r '.[].databaseId | tostring' "$RUNS_FILE" | sort -u > "$STATE_FILE"
    else
        touch "$STATE_FILE"
    fi
fi

processed=$(cat "$STATE_FILE")
n_processed=$(echo "$processed" | grep -c '[0-9]' || true)
echo "watch-ci-flake: loaded $n_processed processed runs, watching $REPO"

is_processed() {
    echo "$processed" | grep -q "^$1$"
}

mark_processed() {
    processed=$(printf '%s\n%s' "$processed" "$1")
    tmp="$STATE_FILE.tmp"
    echo "$processed" | grep '[0-9]' | sort -u > "$tmp"
    mv "$tmp" "$STATE_FILE"
}

process_run() {
    run_id="$1"
    prompt=$(sed "s|{run_id}|$run_id|g; s|{workdir}|$WORKDIR|g; s|{state_dir}|$STATE_DIR|g; s|{hostname}|$(hostname)|g" "$SCRIPT_DIR/watch-ci-flake.md")
    timeout --foreground "$CLAUDE_TIMEOUT" claude --dangerously-skip-permissions --model opus -p "$prompt"
}

if ! gh run list --repo "$REPO" --status failure \
    --json databaseId,url,displayTitle,headBranch,createdAt \
    --limit 100 > "$RUNS_FILE" 2>"$GH_ERR"; then
    echo "watch-ci-flake: gh run list failed:" >&2
    cat "$GH_ERR" >&2
    exit 1
fi

new_ids=$(jq -r 'sort_by(.createdAt) | .[].databaseId | tostring' "$RUNS_FILE")

count=0
for rid in $new_ids; do
    if ! is_processed "$rid"; then
        count=$((count + 1))
    fi
done

now=$(date +%H:%M:%S)
if [ "$count" -eq 0 ]; then
    total=$(echo "$new_ids" | grep -c '[0-9]' || true)
    echo "[$now] poll: $total failures, 0 new"
    exit 0
fi

echo "[$now] $count new failure(s)"

for rid in $new_ids; do
    if is_processed "$rid"; then
        continue
    fi

    title=$(jq -r --arg id "$rid" '.[] | select(.databaseId == ($id | tonumber)) | .displayTitle' "$RUNS_FILE")
    branch=$(jq -r --arg id "$rid" '.[] | select(.databaseId == ($id | tonumber)) | .headBranch' "$RUNS_FILE")
    url=$(jq -r --arg id "$rid" '.[] | select(.databaseId == ($id | tonumber)) | .url' "$RUNS_FILE")

    now=$(date +%H:%M:%S)
    echo "[$now] -> $title ($branch): $url"

    t0=$(date +%s)
    if $DRY_RUN; then
        echo "[$now] (dry-run) would process $rid"
    else
        process_run "$rid" || true
    fi
    elapsed=$(( $(date +%s) - t0 ))

    now=$(date +%H:%M:%S)
    echo "[$now] <- done (${elapsed}s): $url"

    if ! $DRY_RUN; then
        mark_processed "$rid"
    fi
done
