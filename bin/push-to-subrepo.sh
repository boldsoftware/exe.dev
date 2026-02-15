#!/bin/bash
#
# Subrepo sync script. Keeps a "subrepo" in sync with a "main" repo.
#
# Given a range origin/main..HEAD, this filters down the range for commits
# that apply to subrepo.git and pushes those to subrepo/main.
#
# The git magic is that we are keeping subrepo/main^{tree} in sync
# with "git ls-tree origin/main SUBREPO_DIR"; that is, the SUBREPO_DIR subdirectory
# of the main repo has the same contents as the SUBREPO.git repo.
# See also https://blog.philz.dev/blog/git-monorepo/ .
#
# This script is intended to be used as part of the "pushing" code
# in a commit queue (see, e.g, https://sketch.dev/blog/lightweight-merge-queue),
# but can be integrated in other ways.
#
# Note that commit messages from any commits that impact SUBREPO_DIR
# will get copied verbatim, so don't consider those private to the main repo.

set -e
set -o pipefail
set -u

TARGET=$1
SUBREPO=$2
SUBREPO_DIR=$3

[ $TARGET ] || (
    echo "Usage: $0 <target-branch> <subrepo> <subrepo_dir>"
    exit 1
)
[ $SUBREPO ] || (
    echo "Usage: $0 <target-branch> <subrepo> <subrepo_dir>"
    exit 1
)
[ $SUBREPO_DIR ] || (
    echo "Usage: $0 <target-branch> <subrepo> <subrepo_dir>"
    exit 1
)

git remote get-url $SUBREPO || "$SUBREPO must exist as a remote"

# Fail fast if there are any merge commits
if git rev-list --merges "origin/${TARGET}"..HEAD | grep -q .; then
    echo "There are merge commits in the range we're about to push; refusing to continue."
    exit 1
fi

# For every commit we're about to push, we need to port it over to subrepo
PREV_SUBREPO_TREE_OBJ=$(git ls-tree origin/"${TARGET}" $SUBREPO_DIR --format "%(objectname)")
NEED_SUBREPO=0
for c in $(git rev-list --reverse "origin/${TARGET}"..HEAD); do
    echo Pre-processing "$(git log --oneline --decorate -n 1 $c)"
    SUBREPO_TREE_OBJ=$(git ls-tree "$c" $SUBREPO_DIR --format "%(objectname)")
    if [ $SUBREPO_TREE_OBJ != $PREV_SUBREPO_TREE_OBJ ]; then
        NEED_SUBREPO=1
    fi
done

PREV_SUBREPO_TREE_OBJ=$(git ls-tree origin/"${TARGET}" $SUBREPO_DIR --format "%(objectname)")

if [ $NEED_SUBREPO = 0 ]; then
    echo "No changes to ${SUBREPO_DIR}/ folder; nothing to push to ${SUBREPO}."
    exit 0
fi

"$(dirname "$0")/retry.sh" git fetch $SUBREPO

# Assert that the subrepo git tree hasn't moved!
if [ $PREV_SUBREPO_TREE_OBJ != $(git rev-parse "${SUBREPO}/${TARGET}"^{tree}) ]; then
    echo "repo's ${SUBREPO_DIR}/ folder doesn't match ${SUBREPO}; cannot continue"
    exit 1
fi

PREV_SUBREPO_TREE_OBJ=$(git ls-tree origin/"${TARGET}" ${SUBREPO_DIR} --format "%(objectname)")
PREV_SUBREPO_COMMIT=$(git rev-parse "${SUBREPO}"/"${TARGET}")
for c in $(git rev-list --reverse "origin/${TARGET}"..HEAD); do
    echo Processing "$(git log --oneline --decorate -n 1 $c)"
    SUBREPO_TREE_OBJ=$(git ls-tree "$c" ${SUBREPO_DIR} --format "%(objectname)")
    if [ $SUBREPO_TREE_OBJ = $PREV_SUBREPO_TREE_OBJ ]; then
        PREV_SUBREPO_TREE_OBJ=$SUBREPO_TREE_OBJ
        echo "${SUBREPO_DIR}/ folder hasn't changed in commit $c; skipping"
        continue
    fi
    PREV_SUBREPO_TREE_OBJ=$SUBREPO_TREE_OBJ

    GIT_AUTHOR_NAME="$(git log -1 --pretty=format:%an $c)" \
    GIT_AUTHOR_EMAIL="$(git log -1 --pretty=format:%ae $c)" \
    GIT_AUTHOR_DATE="$(git log -1 --pretty=format:%ad $c)" \
    GIT_COMMITTER_NAME="$(git log -1 --pretty=format:%cn $c)" \
    GIT_COMMITTER_EMAIL="$(git log -1 --pretty=format:%ce $c)" \
    GIT_COMMITTER_DATE="$(git log -1 --pretty=format:%cd $c)" \
        git commit-tree $SUBREPO_TREE_OBJ -p $PREV_SUBREPO_COMMIT -m "$(git log -1 --pretty=format:%B $c)" | tee /tmp/${SUBREPO}-commit
    PREV_SUBREPO_COMMIT=$(cat /tmp/${SUBREPO}-commit)
    echo "created commit $PREV_SUBREPO_COMMIT for commit $c"
done

"$(dirname "$0")/retry.sh" --retry-on 128 git push --dry-run $SUBREPO $PREV_SUBREPO_COMMIT:$TARGET
"$(dirname "$0")/retry.sh" --retry-on 128 git push $SUBREPO $(cat /tmp/${SUBREPO}-commit):$TARGET
