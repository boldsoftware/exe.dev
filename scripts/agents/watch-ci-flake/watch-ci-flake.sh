#!/bin/sh
set -e

BK_ORG="bold-software"
BK_PIPELINE="exe-kite-queue"
BK_API="https://buildkite.int.exe.xyz/v2/organizations/$BK_ORG/pipelines/$BK_PIPELINE"
CLAUDE_TIMEOUT=86400 # 24h
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKDIR="$(pwd)"
STATE_DIR="${WATCH_CI_FLAKE_STATE_DIR:-$HOME/watch-ci-flake-state}"

# Autocreate state directory
mkdir -p "$STATE_DIR"

STATE_FILE="$STATE_DIR/.state"
BUILDS_FILE=$(mktemp)
trap 'rm -f "$BUILDS_FILE"' EXIT INT TERM

DRY_RUN=false
for arg in "$@"; do
    case "$arg" in
    --dry-run) DRY_RUN=true ;;
    esac
done

bk_get() {
    curl -sS "$@"
}

# On first start (no state file), snapshot current failures so we only process future ones.
if [ ! -f "$STATE_FILE" ]; then
    echo "watch-ci-flake: first run, snapshotting existing failures..."
    if bk_get "$BK_API/builds?state=failed&per_page=100" >"$BUILDS_FILE" 2>/dev/null; then
        jq -r '.[].number | tostring' "$BUILDS_FILE" | sort -u >"$STATE_FILE"
    else
        touch "$STATE_FILE"
    fi
fi

processed=$(cat "$STATE_FILE")
n_processed=$(echo "$processed" | grep -c '[0-9]' || true)
echo "watch-ci-flake: loaded $n_processed processed builds, watching $BK_ORG/$BK_PIPELINE"

is_processed() {
    echo "$processed" | grep -q "^$1$"
}

mark_processed() {
    processed=$(printf '%s\n%s' "$processed" "$1")
    tmp="$STATE_FILE.tmp"
    echo "$processed" | grep '[0-9]' | sort -u >"$tmp"
    mv "$tmp" "$STATE_FILE"
}

process_build() {
    build_number="$1"
    prompt=$(sed "s|{build_number}|$build_number|g; s|{workdir}|$WORKDIR|g; s|{state_dir}|$STATE_DIR|g; s|{hostname}|$(hostname)|g" "$SCRIPT_DIR/watch-ci-flake.md")
    timeout --foreground "$CLAUDE_TIMEOUT" claude --dangerously-skip-permissions --model opus -p "$prompt"
}

if ! bk_get "$BK_API/builds?state=failed&per_page=100" >"$BUILDS_FILE" 2>/dev/null; then
    echo "watch-ci-flake: buildkite API request failed" >&2
    exit 1
fi

new_numbers=$(jq -r 'sort_by(.created_at) | .[].number | tostring' "$BUILDS_FILE")

count=0
for num in $new_numbers; do
    if ! is_processed "$num"; then
        count=$((count + 1))
    fi
done

now=$(date +%H:%M:%S)
if [ "$count" -eq 0 ]; then
    total=$(echo "$new_numbers" | grep -c '[0-9]' || true)
    echo "[$now] poll: $total failures, 0 new"
    exit 0
fi

echo "[$now] $count new failure(s)"

for num in $new_numbers; do
    if is_processed "$num"; then
        continue
    fi

    message=$(jq -r --arg n "$num" '.[] | select(.number == ($n | tonumber)) | .message' "$BUILDS_FILE" | head -1)
    branch=$(jq -r --arg n "$num" '.[] | select(.number == ($n | tonumber)) | .branch' "$BUILDS_FILE")
    web_url=$(jq -r --arg n "$num" '.[] | select(.number == ($n | tonumber)) | .web_url' "$BUILDS_FILE")

    now=$(date +%H:%M:%S)
    echo "[$now] -> $message ($branch): $web_url"

    t0=$(date +%s)
    if $DRY_RUN; then
        echo "[$now] (dry-run) would process $num"
    else
        process_build "$num" || true
    fi
    elapsed=$(($(date +%s) - t0))

    now=$(date +%H:%M:%S)
    echo "[$now] <- done (${elapsed}s): $web_url"

    if ! $DRY_RUN; then
        mark_processed "$num"
    fi
done
