#!/bin/bash
#
# sync-commits-from-oss.sh - Sync commits from exe.dev.git to exe.git
#
# This script takes commits from exe.dev.git (the "oss" remote) and creates
# corresponding commits in exe.git that update the oss/ subdirectory to match.
#
# The invariant maintained is: OSS_COMMIT^{tree} should equal the tree object
# referenced by "git ls-tree origin/main oss".
#
# Usage:
#   git remote add oss git@github.com:boldsoftware/exe.dev.git
#   git fetch oss
#   sync-commits-from-oss.sh [OSS_COMMIT, e.g., oss/main]
#
# And then, you can "git push origin new-exe-commit-oss:main" if you
# like what you see.
#
# Arguments:
#   OSS_COMMIT  The oss commit to sync to (default: oss/main)
#
# Script follows the following logic:
# 1. Check if already in sync - if so, exit successfully
# 2. Find the "base" commit in oss history that matches current exe oss/ tree
# 3. Process commits from base to target (max 20, no merges)
# 4. For each commit, create a new commit in exe.git with updated oss/ directory
# 5. Tag the final commit and show summary

set -e
set -o pipefail
set -u

# Parse command line arguments
OSS_COMMIT="${1:-oss/main}"

# Step 1: Check if already in sync
echo "Checking if $OSS_COMMIT and exe oss/ are already in sync..."

# Get the tree hash of the oss commit
OSS_TREE=$(git rev-parse "$OSS_COMMIT"^{tree})
echo "$OSS_COMMIT^{tree}: $OSS_TREE"

# Get the tree hash of the oss directory in exe's main
EXE_OSS_TREE=$(git ls-tree origin/main oss --format "%(objectname)")
echo "exe oss/ tree: $EXE_OSS_TREE"

if [ "$OSS_TREE" = "$EXE_OSS_TREE" ]; then
    echo "Already in sync! Nothing to do."
    exit 0
fi

echo "Trees differ - need to sync commits from oss to exe"

# Step 2: Find the base commit
echo "Finding base commit in $OSS_COMMIT history..."

BASE_COMMIT=""
COMMIT_COUNT=0
for commit_info in $(git log -n 20 --pretty="%H:%T" "$OSS_COMMIT"); do
    commit_hash=$(echo $commit_info | awk -F: '{ print $1 }')
    tree_hash=$(echo $commit_info | awk -F: '{ print $2 }')
    COMMIT_COUNT=$((COMMIT_COUNT + 1))
    echo "Checking commit: $commit_hash (tree: $tree_hash)"

    if [ "$tree_hash" = "$EXE_OSS_TREE" ]; then
        BASE_COMMIT="$commit_hash"
        echo "Found base commit: $BASE_COMMIT (tree: $tree_hash) after checking $COMMIT_COUNT commits"
        break
    fi
done

if [ -z "$BASE_COMMIT" ]; then
    echo "ERROR: Could not find base commit in $OSS_COMMIT history that matches exe oss/ tree (searched $COMMIT_COUNT commits)"
    echo "The exe repo's oss/ directory may be too out of date; check it manually."
    exit 1
fi

# Step 3: Get commits to process
echo "Getting commits between base and $OSS_COMMIT..."

# Count commits first
COMMIT_COUNT=$(git rev-list --count "$BASE_COMMIT".."$OSS_COMMIT")
echo "Found $COMMIT_COUNT commits to process"

if [ $COMMIT_COUNT -eq 0 ]; then
    echo "No commits to process - already at latest"
    exit 0
fi

if [ $COMMIT_COUNT -gt 20 ]; then
    echo "ERROR: Too many commits to process ($COMMIT_COUNT > 20). This seems dangerous."
    exit 1
fi

# Check for merge commits
echo "Checking for merge commits..."
MERGE_COMMITS=$(git rev-list --merges "$BASE_COMMIT".."$OSS_COMMIT")
if [ -n "$MERGE_COMMITS" ]; then
    echo "ERROR: Found merge commits in range. Refusing to continue."
    echo "Merge commits found:"
    git log --oneline --merges "$BASE_COMMIT".."$OSS_COMMIT"
    exit 1
fi

echo "All commits are non-merge commits - proceeding"

# Step 4: Process each commit
CURRENT_EXE_COMMIT=$(git rev-parse HEAD)
echo "Starting from exe commit: $CURRENT_EXE_COMMIT"

FIRST_NEW_COMMIT=""
for commit in $(git rev-list --reverse "$BASE_COMMIT".."$OSS_COMMIT"); do
    echo "Processing oss commit: $(git log --oneline --decorate -n 1 $commit)"

    # Get the tree of this oss commit
    OSS_COMMIT_TREE=$(git rev-parse "$commit^{tree}")
    echo "  Oss commit tree: $OSS_COMMIT_TREE"

    # Create a new tree that combines:
    # - All entries from current exe HEAD except oss/
    # - The oss/ entry pointing to the oss commit tree

    # Get the top-level tree entries from current HEAD, excluding oss/
    TEMP_INDEX=$(mktemp)
    git ls-tree HEAD | grep -v $'\toss$' >"$TEMP_INDEX" || true

    # Add the oss directory entry
    printf "040000 tree %s\toss\n" "$OSS_COMMIT_TREE" >>"$TEMP_INDEX"

    # Create tree from this index
    NEW_TREE=$(git mktree <"$TEMP_INDEX")
    rm "$TEMP_INDEX"

    echo "  Created new tree: $NEW_TREE"

    # Create commit with same metadata as original oss commit
    new_commit_file=$(mktemp)
    GIT_AUTHOR_NAME="$(git log -1 --pretty=format:%an $commit)" \
    GIT_AUTHOR_EMAIL="$(git log -1 --pretty=format:%ae $commit)" \
    GIT_AUTHOR_DATE="$(git log -1 --pretty=format:%ad $commit)" \
    GIT_COMMITTER_NAME="$(git log -1 --pretty=format:%cn $commit)" \
    GIT_COMMITTER_EMAIL="$(git log -1 --pretty=format:%ce $commit)" \
    GIT_COMMITTER_DATE="$(git log -1 --pretty=format:%cd $commit)" \
        git commit-tree "$NEW_TREE" -p "$CURRENT_EXE_COMMIT" -m "$(git log -1 --pretty=format:%B $commit)" | tee $new_commit_file
    NEW_COMMIT=$(cat $new_commit_file)

    echo "  Created exe commit: $NEW_COMMIT"

    # Track the first new commit for the log range
    if [ -z "$FIRST_NEW_COMMIT" ]; then
        FIRST_NEW_COMMIT="$NEW_COMMIT"
    fi

    # Update our current position
    CURRENT_EXE_COMMIT="$NEW_COMMIT"
done

# Tag the final commit
echo "Tagging final commit: $CURRENT_EXE_COMMIT"
git tag -f new-exe-commit-oss "$CURRENT_EXE_COMMIT"

# Step 5: Verify final state
echo "Verifying final state..."

FINAL_OSS_TREE=$(git rev-parse "$OSS_COMMIT"^{tree})
FINAL_EXE_OSS_TREE=$(git ls-tree "$CURRENT_EXE_COMMIT" oss --format "%(objectname)")

if [ "$FINAL_OSS_TREE" = "$FINAL_EXE_OSS_TREE" ]; then
    echo "Success! Trees now match:"
    echo "  $OSS_COMMIT^{tree}: $FINAL_OSS_TREE"
    echo "  exe oss/ tree: $FINAL_EXE_OSS_TREE"
    echo "Processed $COMMIT_COUNT commits successfully."
    echo
    echo "New commits created:"
    git log --boundary --oneline "$FIRST_NEW_COMMIT"^.."$CURRENT_EXE_COMMIT"
    echo
    echo "Final commit tagged as 'new-exe-commit-oss': $CURRENT_EXE_COMMIT"
else
    echo "Verification failed! Trees still don't match:"
    echo "  $OSS_COMMIT^{tree}: $FINAL_OSS_TREE"
    echo "  exe oss/ tree: $FINAL_EXE_OSS_TREE"
    exit 1
fi
