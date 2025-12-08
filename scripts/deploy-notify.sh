#!/bin/bash
#
# Slack deployment notification helpers (best-effort, never fails deployment).
#
# Usage from Makefile or other scripts:
#   DEPLOY_TS=$(./scripts/deploy-notify.sh start exed)
#   # ... do deployment ...
#   ./scripts/deploy-notify.sh complete "$DEPLOY_TS"
#
# Or on failure:
#   ./scripts/deploy-notify.sh fail "$DEPLOY_TS"
#
# Requires: EXE_SLACK_BOT_TOKEN environment variable

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

check_token() {
    if [ -z "$EXE_SLACK_BOT_TOKEN" ]; then
        echo "Slack notifications disabled. Set EXE_SLACK_BOT_TOKEN to post to #ship." >&2
        return 1
    fi
    return 0
}

deploy_notify_start() {
    local service="$1"

    if ! check_token; then
        return 0
    fi

    local sha
    local deployer
    local dirty_flag=""

    sha=$(git rev-parse --short HEAD)
    deployer=$(whoami)

    if ! git diff --quiet HEAD 2>/dev/null || ! git diff --cached --quiet HEAD 2>/dev/null; then
        dirty_flag="--dirty"
    fi

    uv run "$SCRIPT_DIR/slack/deploy_notify.py" start \
        --service "$service" \
        --sha "$sha" \
        --deployer "$deployer" \
        $dirty_flag 2>/dev/null || true
}

deploy_notify_complete() {
    local ts="$1"
    if [ -z "$ts" ] || ! check_token; then
        return 0
    fi
    uv run "$SCRIPT_DIR/slack/deploy_notify.py" complete --ts "$ts" 2>/dev/null || true
}

deploy_notify_fail() {
    local ts="$1"
    if [ -z "$ts" ] || ! check_token; then
        return 0
    fi
    uv run "$SCRIPT_DIR/slack/deploy_notify.py" fail --ts "$ts" 2>/dev/null || true
}

# When called directly (not sourced), execute command
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    case "$1" in
        start)
            if [ -z "$2" ]; then
                echo "Usage: $0 start <service>" >&2
                exit 0
            fi
            deploy_notify_start "$2"
            ;;
        complete)
            if [ -z "$2" ]; then
                echo "Usage: $0 complete <ts>" >&2
                exit 0
            fi
            deploy_notify_complete "$2"
            ;;
        fail)
            if [ -z "$2" ]; then
                echo "Usage: $0 fail <ts>" >&2
                exit 0
            fi
            deploy_notify_fail "$2"
            ;;
        *)
            echo "Usage: $0 {start|complete|fail} <args>" >&2
            echo "" >&2
            echo "Commands:" >&2
            echo "  start <service>  - Post deployment start message, prints ts to stdout" >&2
            echo "  complete <ts>    - Add checkmark emoji to message" >&2
            echo "  fail <ts>        - Add X emoji to message" >&2
            exit 0
            ;;
    esac
fi
