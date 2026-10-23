#!/bin/sh
# Release exe-tty from CI (see .github/workflows/test.yml).
#
# The auto-tag job tags exe-tty/v... when exe-tty/ changes; this publishes
# binaries for the tag at HEAD. Skips (exit 0) when there is none, or it is
# already released, so re-runs are safe.
#
# Requires: tags fetched, mise, and gh with GH_TOKEN.
set -e
cd "$(dirname -- "$0")"

TAG=$(git tag -l 'exe-tty/v*' --points-at HEAD | head -1)
if [ -z "$TAG" ] || gh release view "$TAG" >/dev/null 2>&1; then
    echo "no unreleased exe-tty tag at HEAD; nothing to release."
    exit 0
fi

rm -rf dist
mkdir dist
for os in linux darwin; do
    for arch in amd64 arm64; do
        rm -f exescroll/binary/exe-scroll-* # embed only this platform's
        make GOOS=$os GOARCH=$arch VERSION="${TAG#exe-tty/}" exe-scroll all
        mv exe-tty "dist/exe-tty-$os-$arch"
    done
done
V=$(./dist/exe-tty-linux-amd64 -version)
case "$V" in
"exe-tty ${TAG#exe-tty/}, with exe-scroll/"*) ;;
*)
    echo "unexpected version: $V" >&2
    exit 1
    ;;
esac

gh release create "$TAG" --title "$TAG" \
    --notes "$V, built from $(git rev-parse --short HEAD)." \
    dist/exe-tty-*
