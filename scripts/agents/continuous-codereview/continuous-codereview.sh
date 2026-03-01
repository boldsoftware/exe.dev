#!/bin/sh
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
STATE_DIR="${CONTINUOUS_CODEREVIEW_STATE_DIR:-$HOME/continuous-codereview-state}"
LOCKFILE="$STATE_DIR/.lock"
# Hard ceiling: kill the run if it's still going after 20 minutes.
# The Python script has internal timeouts (review 600s, sanity check 300s),
# but git/gh calls don't. This is the backstop that prevents a hung run
# from holding the flock forever.
RUN_TIMEOUT=1200

mkdir -p "$STATE_DIR"

# Acquire lock (non-blocking). If a previous run is still going, skip quietly.
exec 9>"$LOCKFILE"
if ! flock --nonblock 9; then
    echo "continuous-codereview: previous run still active, skipping"
    exit 0
fi

exec timeout --foreground "$RUN_TIMEOUT" python3 "$SCRIPT_DIR/continuous_codereview.py" "$@"
