#!/bin/bash
# Check that the currently deployed SHA exists locally before deploying.
# This prevents accidentally rolling back someone else's code when deploying
# from a local git repo that hasn't been fetched recently enough.
#
# Usage: check-remote-sha.sh <debug-url>
#
# Environment variables:
#   IGNORE_NONLOCAL_COMMIT=1: Override the check

set -e

if [ "$IGNORE_NONLOCAL_COMMIT" = "1" ]; then
    exit 0
fi

if [ -z "$1" ]; then
    echo "ERROR: debug URL required" >&2
    echo "Usage: $0 <debug-url>" >&2
    exit 1
fi

DEBUG_URL="$1"

RED='\033[0;31m'
NC='\033[0m'

# Try to fetch the currently deployed SHA
REMOTE_SHA=$(curl -s --connect-timeout 5 "$DEBUG_URL" 2>/dev/null || true)

# If we couldn't reach the endpoint, fail open
if [ -z "$REMOTE_SHA" ]; then
    exit 0
fi

# Validate it looks like a SHA (7-40 hex chars)
if ! echo "$REMOTE_SHA" | grep -qE '^[0-9a-f]{7,40}$'; then
    exit 0
fi

# Check if the SHA exists locally
if ! git cat-file -e "${REMOTE_SHA}^{commit}" 2>/dev/null; then
    echo -e "${RED}ERROR: Currently deployed SHA $REMOTE_SHA does not exist in your local repo.${NC}" >&2
    echo "" >&2
    echo "Deploying now might accidentally roll back some changes." >&2
    echo "git fetch might help." >&2
    echo "" >&2
    echo "To override this error, retry with env var IGNORE_NONLOCAL_COMMIT=1." >&2
    exit 1
fi

# Check that the deployed SHA is an ancestor of HEAD (no going backwards).
# This prevents deploying an older commit on top of a newer one.
if ! git merge-base --is-ancestor "$REMOTE_SHA" HEAD 2>/dev/null; then
    if [ "$ALLOW_ROLLBACK" = "1" ]; then
        echo -e "${RED}WARNING: Deploying older commit than what's running (ALLOW_ROLLBACK=1)${NC}" >&2
    else
        echo -e "${RED}ERROR: Currently deployed $REMOTE_SHA is not an ancestor of HEAD.${NC}" >&2
        echo "" >&2
        echo "This would roll back the deployment. If that's intentional," >&2
        echo "retry with env var ALLOW_ROLLBACK=1." >&2
        exit 1
    fi
fi
