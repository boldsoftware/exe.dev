#!/bin/bash
# Check deployment safety conditions for production deploys
# Usage: source this script or call it directly
# Pass -f as first argument to force deploy despite warnings

FORCE=0
if [ "$1" = "-f" ] || [ "$FORCE_DEPLOY" = "1" ]; then
    FORCE=1
fi

RED='\033[0;31m'
NC='\033[0m'

errors=0

# Check for git worktree (go build has issues with worktrees: go.dev/issue/58218)
# In a worktree, git-dir and git-common-dir differ
if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
    echo -e "${RED}ERROR: Cannot deploy from a git worktree (go.dev/issue/58218).${NC}" >&2
    echo "Please deploy from the main repository checkout." >&2
    exit 1
fi

# Check for dirty worktree (ignore untracked files)
if [ -n "$(git status --porcelain | grep -v '^??')" ]; then
    if [ "$FORCE" = "1" ]; then
        echo -e "${RED}WARNING: Deploying from dirty worktree (forced)${NC}" >&2
    else
        echo -e "${RED}ERROR: Dirty worktree. Commit or stash changes before deploying to production.${NC}" >&2
        echo "Use -f to force deploy anyway." >&2
        errors=1
    fi
fi

# Check if origin/main is an ancestor of HEAD (HEAD contains all of origin/main)
if ! git merge-base --is-ancestor origin/main HEAD 2>/dev/null; then
    if [ "$FORCE" = "1" ]; then
        echo -e "${RED}WARNING: Deploying commit not on origin/main (forced)${NC}" >&2
    else
        echo -e "${RED}ERROR: HEAD is not descended from origin/main.${NC}" >&2
        echo "Push your changes to main or use -f to force deploy anyway." >&2
        errors=1
    fi
fi

if [ "$errors" -ne 0 ]; then
    exit 1
fi
