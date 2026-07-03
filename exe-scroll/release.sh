#!/bin/sh
# Tag and release exe-scroll from CI (see .github/workflows/test.yml).
#
# Versioned by the number of commits touching exe-scroll/, matching the Go
# module auto-tagging scheme in test.yml: exe-scroll/v0.<count>.9<sha-octal>.
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

COUNT=$(git rev-list --count HEAD -- exe-scroll/)
SHORT_SHA=$(git rev-parse --short=6 HEAD)
SHA_OCTAL=$(printf '%o' "0x${SHORT_SHA}")
TAG="exe-scroll/v0.${COUNT}.9${SHA_OCTAL}"

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
