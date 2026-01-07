#!/bin/bash
#
# sync-commits-from-shelley.sh - Sync commits from shelley.git to exe.git
#
# This script takes commits from shelley.git and creates corresponding commits
# in exe.git that update the shelley/ subdirectory to match.
#
# The invariant maintained is: SHELLEY_COMMIT^{tree} should equal the tree object
# referenced by "git ls-tree origin/main shelley".
#
# Usage:
#   git remote add shelley git@github.com:boldsoftware/shelley.git
#   git fetch shelley
#   sync-commits-from-shelley.sh [SHELLEY_COMMIT, e.g., shelley/main]
#
# And then, you can "git push origin new-exe-commit:main" if you
# like what you see.
#
# Arguments:
#   SHELLEY_COMMIT  The shelley commit to sync to (default: shelley/main)
#
# Script follows the following logic:
# 1. Check if already in sync - if so, exit successfully
# 2. Find the "base" commit in shelley history that matches current exe shelley/ tree
# 3. Process commits from base to target (max 20, no merges)
# 4. For each commit, create a new commit in exe.git with updated shelley/ directory
# 5. Tag the final commit and show summary

set -e
set -o pipefail
set -u

# Parse command line arguments
SHELLEY_COMMIT="${1:-shelley/main}"

# Step 1: Check if already in sync
echo "Checking if $SHELLEY_COMMIT and exe shelley/ are already in sync..."

# Get the tree hash of the shelley commit
SHELLEY_TREE=$(git rev-parse "$SHELLEY_COMMIT"^{tree})
echo "$SHELLEY_COMMIT^{tree}: $SHELLEY_TREE"

# Get the tree hash of the shelley directory in exe3's main
EXE_SHELLEY_TREE=$(git ls-tree origin/main shelley --format "%(objectname)")
echo "exe shelley/ tree: $EXE_SHELLEY_TREE"

if [ "$SHELLEY_TREE" = "$EXE_SHELLEY_TREE" ]; then
	echo "Already in sync! Nothing to do."
	exit 0
fi

echo "Trees differ - need to sync commits from shelley to exe3"

# Step 2: Find the base commit
echo "Finding base commit in $SHELLEY_COMMIT history..."

BASE_COMMIT=""
COMMIT_COUNT=0
for commit_info in $(git log -n 20 --pretty="%H:%T" "$SHELLEY_COMMIT"); do
	commit_hash=$(echo $commit_info | awk -F: '{ print $1 }')
	tree_hash=$(echo $commit_info | awk -F: '{ print $2 }')
	COMMIT_COUNT=$((COMMIT_COUNT + 1))
	echo "Checking commit: $commit_hash (tree: $tree_hash)"

	if [ "$tree_hash" = "$EXE_SHELLEY_TREE" ]; then
		BASE_COMMIT="$commit_hash"
		echo "Found base commit: $BASE_COMMIT (tree: $tree_hash) after checking $COMMIT_COUNT commits"
		break
	fi
done

if [ -z "$BASE_COMMIT" ]; then
	echo "ERROR: Could not find base commit in $SHELLEY_COMMIT history that matches exe shelley/ tree (searched $COMMIT_COUNT commits)"
	echo "The exe repo's shelley/ directory may be too out of date; check it manually."
	exit 1
fi

# Step 3: Get commits to process
echo "Getting commits between base and $SHELLEY_COMMIT..."

# Count commits first
COMMIT_COUNT=$(git rev-list --count "$BASE_COMMIT".."$SHELLEY_COMMIT")
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
MERGE_COMMITS=$(git rev-list --merges "$BASE_COMMIT".."$SHELLEY_COMMIT")
if [ -n "$MERGE_COMMITS" ]; then
	echo "ERROR: Found merge commits in range. Refusing to continue."
	echo "Merge commits found:"
	git log --oneline --merges "$BASE_COMMIT".."$SHELLEY_COMMIT"
	exit 1
fi

echo "All commits are non-merge commits - proceeding"

# Step 4: Process each commit
CURRENT_EXE_COMMIT=$(git rev-parse HEAD)
echo "Starting from exe commit: $CURRENT_EXE_COMMIT"

FIRST_NEW_COMMIT=""
for commit in $(git rev-list --reverse "$BASE_COMMIT".."$SHELLEY_COMMIT"); do
	echo "Processing shelley commit: $(git log --oneline --decorate -n 1 $commit)"

	# Get the tree of this shelley commit
	SHELLEY_COMMIT_TREE=$(git rev-parse "$commit^{tree}")
	echo "  Shelley commit tree: $SHELLEY_COMMIT_TREE"

	# Create a new tree that combines:
	# - All entries from current exe3 HEAD except shelley/
	# - The shelley/ entry pointing to the shelley commit tree

	# Get the top-level tree entries from current HEAD, excluding shelley/
	TEMP_INDEX=$(mktemp)
	git ls-tree HEAD | grep -v $'\tshelley$' >"$TEMP_INDEX" || true

	# Add the shelley directory entry
	printf "040000 tree %s\tshelley\n" "$SHELLEY_COMMIT_TREE" >>"$TEMP_INDEX"

	# Create tree from this index
	NEW_TREE=$(git mktree <"$TEMP_INDEX")
	rm "$TEMP_INDEX"

	echo "  Created new tree: $NEW_TREE"

	# Create commit with same metadata as original shelley commit
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
git tag -f new-exe-commit "$CURRENT_EXE_COMMIT"

# Step 5: Verify final state
echo "Verifying final state..."

FINAL_SHELLEY_TREE=$(git rev-parse "$SHELLEY_COMMIT"^{tree})
FINAL_EXE_SHELLEY_TREE=$(git ls-tree "$CURRENT_EXE_COMMIT" shelley --format "%(objectname)")

if [ "$FINAL_SHELLEY_TREE" = "$FINAL_EXE_SHELLEY_TREE" ]; then
	echo "Success! Trees now match:"
	echo "  $SHELLEY_COMMIT^{tree}: $FINAL_SHELLEY_TREE"
	echo "  exe shelley/ tree: $FINAL_EXE_SHELLEY_TREE"
	echo "Processed $COMMIT_COUNT commits successfully."
	echo
	echo "New commits created:"
	git log --boundary --oneline "$FIRST_NEW_COMMIT"^.."$CURRENT_EXE_COMMIT"
	echo
	echo "Final commit tagged as 'new-exe-commit': $CURRENT_EXE_COMMIT"
else
	echo "Verification failed! Trees still don't match:"
	echo "  $SHELLEY_COMMIT^{tree}: $FINAL_SHELLEY_TREE"
	echo "  exe shelley/ tree: $FINAL_EXE_SHELLEY_TREE"
	exit 1
fi
