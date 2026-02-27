#!/bin/bash
# Check whether an environment is locked before deploying.
# If the lock server is unreachable or returns an error, fail closed.
# Set DEPLOY_SKIP_PRODLOCK=1 to bypass.
#
# Usage: check-prodlock.sh <env>
#   env: "prod" or "staging"

ENV="$1"
if [ "$ENV" != "prod" ] && [ "$ENV" != "staging" ]; then
    echo "Usage: $0 <prod|staging>" >&2
    exit 1
fi

RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

if [ "$DEPLOY_SKIP_PRODLOCK" = "1" ]; then
    echo -e "${RED}WARNING: Skipping deploy lock check (DEPLOY_SKIP_PRODLOCK=1)${NC}" >&2
    exit 0
fi

TOKEN="exe0.e30.U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgx-S5l-ozGMZeV-_n8rEIWO6vyzY3wqtMFRU3eT8IqygAAAATdjBAcHJvZGxvY2suZXhlLnh5egAAAAAAAAAGc2hhNTEyAAAAUwAAAAtzc2gtZWQyNTUxOQAAAECzPY2tfopxr8qAZRhl4TwiEaEOXyq_9kWSP2xWag5oqwPdYnbuHffHigcem6ImJiIOFxB2QaDg4gfT4xI6x6UH"

RESPONSE=$(curl -sf --max-time 10 \
    -H "Authorization: Bearer $TOKEN" \
    "https://prodlock.exe.xyz:8000/api/${ENV}" 2>&1)

if [ $? -ne 0 ]; then
    echo -e "${RED}ERROR: Could not reach prodlock server (https://prodlock.exe.xyz:8000/api/${ENV}).${NC}" >&2
    echo "The server may be down, or the request timed out." >&2
    echo "Refusing to deploy without confirming ${ENV} is unlocked." >&2
    echo "Set DEPLOY_SKIP_PRODLOCK=1 to bypass this check." >&2
    exit 1
fi

if [ "$(echo "$RESPONSE" | jq -r '.locked')" = "true" ]; then
    LOCKED_BY=$(echo "$RESPONSE" | jq -r '.locked_by // "unknown"')
    SINCE=$(echo "$RESPONSE" | jq -r '.since // "unknown"')
    echo -e "${RED}ERROR: ${ENV} is locked.${NC}" >&2
    echo "Locked by: $LOCKED_BY" >&2
    echo "Since:     $SINCE" >&2
    echo "Set DEPLOY_SKIP_PRODLOCK=1 to bypass this check." >&2
    exit 1
fi
