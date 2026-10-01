#!/bin/sh
# Tag and release exe-scroll from CI (see .github/workflows/test.yml).
#
# Versioned by the number of commits touching exe-scroll/, like the Go module
# auto-tagging scheme in test.yml, but with the tree hash in place of the
# commit's: exe-scroll/v0.<count>.9<tree-octal>.
# Skips (exit 0) when exe-scroll/ is unchanged since the latest release tag.
#
# Requires: full git history + tags (actions/checkout fetch-depth: 0), gh CLI
# with GH_TOKEN, and push access to tags (permissions: contents: write).
set -e

SRC_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$SRC_DIR/.."

# Skip when exe-scroll/ is unchanged since the latest released tag. Both
# conditions matter: checking the release (not just the tag) lets a re-run
# recover if a previous run pushed the tag but died before publishing.
LATEST=$(git tag -l 'exe-scroll/v*' --sort=-v:refname | head -1)
if [ -n "$LATEST" ] && git diff --quiet "$LATEST" -- exe-scroll/ &&
    gh release view "$LATEST" >/dev/null 2>&1; then
    echo "exe-scroll unchanged since released $LATEST; nothing to release."
    exit 0
fi

# The tag is the version the binary reports (sourceVersion in build.zig), so
# its last part encodes exe-scroll/'s tree hash, not the commit's.
COUNT=$(git rev-list --count HEAD -- exe-scroll/)
SHORT_SHA=$(git rev-parse --short=6 HEAD)
TREE=$(git rev-parse HEAD:exe-scroll | cut -c1-6)
TAG="exe-scroll/v0.${COUNT}.9$(printf '%o' "0x${TREE}")"

# Key idempotency on the release, not the tag: if a previous run pushed the
# tag but died before creating the release, a re-run still finishes the job.
if gh release view "$TAG" >/dev/null 2>&1; then
    echo "release $TAG already exists; nothing to release."
    exit 0
fi

echo "Releasing $TAG"

DIST="$SRC_DIR/dist"
rm -rf "$DIST"
for arch in amd64 arm64; do
    OUT_DIR="$DIST/$arch" "$SRC_DIR/build-static.sh" "$arch"
    cp "$DIST/$arch/bin/exe-scroll" "$DIST/exe-scroll-linux-$arch"
done

# The binaries must report the tag (where this host can run one), or the
# build saw a dirty tree.
if got=$("$DIST/exe-scroll-linux-amd64" --version 2>/dev/null) &&
    [ "${got##* }" != "${TAG#exe-scroll/v}" ]; then
    echo "release.sh: binary reports $got, want ${TAG#exe-scroll/v}" >&2
    exit 1
fi

if ! git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
    git config user.name "github-actions[bot]"
    git config user.email "github-actions[bot]@users.noreply.github.com"
    git tag -a "$TAG" -m "exe-scroll release $TAG (${SHORT_SHA})"
    git push origin "$TAG"
fi

gh release create "$TAG" \
    --title "$TAG" \
    --notes "Static musl builds of exe-scroll at ${SHORT_SHA}." \
    "$DIST"/exe-scroll-linux-*
