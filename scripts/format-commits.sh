#!/bin/bash
# Rebase local commits on main, inserting formatting commits after any commit
# that produces changes from formatters/generators.
#
# Usage: scripts/format-commits.sh
#
# This script:
# 1. Fails if not on main branch or git state is dirty
# 2. For each commit between origin/main and main, runs all formatters/generators
# 3. If changes are created, inserts a new commit directly after
#
# After running, review the formatting commits and squash them into their parents.

set -euo pipefail

RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m'

die() {
    echo -e "${RED}ERROR: $1${NC}" >&2
    exit 1
}

# Check we're on main branch
current_branch=$(git rev-parse --abbrev-ref HEAD)
if [ "$current_branch" != "main" ]; then
    die "Must be on main branch (currently on '$current_branch')"
fi

# Check for dirty worktree (including untracked files that aren't in .gitignore)
if [ -n "$(git status --porcelain)" ]; then
    die "Git state is dirty. Commit or stash all changes first."
fi

# Ensure shelley/ui dependencies are installed (needed for prettier)
if [ ! -d "shelley/ui/node_modules" ]; then
    echo "Installing shelley/ui dependencies..."
    (cd shelley/ui && pnpm install --frozen-lockfile)
fi

# Fetch to ensure origin/main is up to date
git fetch origin main

# Get commits between origin/main and main (oldest first)
commits=$(git rev-list --reverse origin/main..main)

if [ -z "$commits" ]; then
    echo "No commits between origin/main and main. Nothing to do."
    exit 0
fi

commit_count=$(echo "$commits" | wc -l | tr -d ' ')
echo "Found $commit_count commit(s) to process"

# Save current HEAD for potential recovery
original_head=$(git rev-parse HEAD)

# Start from origin/main
git reset --hard origin/main

format_commits_added=0

for commit in $commits; do
    # Get the commit message for display
    short_sha=$(git rev-parse --short "$commit")
    subject=$(git log -1 --format='%s' "$commit")
    echo -e "\n${YELLOW}Processing: $short_sha $subject${NC}"

    # Cherry-pick the commit
    if ! git cherry-pick "$commit"; then
        echo -e "${RED}Cherry-pick failed. Aborting.${NC}" >&2
        echo "To recover, run: git reset --hard $original_head" >&2
        exit 1
    fi

    # Run all formatters and generators (same as CI)
    echo "  Running go generate ./..."
    go generate ./...

    echo "  Running bin/run_formatters.sh..."
    ./bin/run_formatters.sh

    # Check if there are changes
    if [ -n "$(git status --porcelain)" ]; then
        echo -e "${YELLOW}  -> Formatting changes detected, creating fixup commit${NC}"
        git add -A
        git commit -m "fixup! $subject

go generate + formatters"
        format_commits_added=$((format_commits_added + 1))
    else
        echo "  -> No formatting changes"
    fi
done

echo
if [ "$format_commits_added" -gt 0 ]; then
    echo -e "${YELLOW}Done. Added $format_commits_added formatting commit(s).${NC}"
    echo "Review and squash with: git rebase -i --autosquash origin/main"
else
    echo "Done. No formatting commits needed."
fi
