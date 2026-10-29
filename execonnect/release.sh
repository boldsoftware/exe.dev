#!/bin/sh
# Release execonnect from CI (see .github/workflows/test.yml).
#
# The auto-tag job tags execonnect/v... when execonnect/ changes; this
# publishes binaries for the tag at HEAD. Skips (exit 0) when there is none,
# or it is already released, so re-runs are safe.
#
# Requires: tags fetched, Go, and gh with GH_TOKEN.
set -e
cd "$(dirname -- "$0")"

TAG=$(git tag -l 'execonnect/v*' --points-at HEAD | head -1)
if [ -z "$TAG" ] || gh release view "$TAG" >/dev/null 2>&1; then
    echo "no unreleased execonnect tag at HEAD; nothing to release."
    exit 0
fi
VERSION=${TAG#execonnect/}

rm -rf dist
mkdir dist
for os in linux darwin; do
    for arch in amd64 arm64; do
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
            -ldflags "-s -w -X github.com/boldsoftware/exe.dev/execonnect.Version=$VERSION" \
            -o "dist/execonnect-$os-$arch" ./cmd/execonnect
    done
done
V=$(./dist/execonnect-linux-amd64 --version)
case "$V" in
"execonnect version $VERSION ("*")") ;;
*)
    echo "unexpected version: $V" >&2
    exit 1
    ;;
esac

# Linux tarballs bundle the systemd unit and installer with the binary.
for arch in amd64 arm64; do
    pkg=execonnect-linux-$arch
    mkdir -p "dist/stage/$pkg"
    cp "dist/$pkg" "dist/stage/$pkg/execonnect"
    cp packaging/systemd/execonnect.service packaging/systemd/install.sh \
        LICENSE NOTICE "dist/stage/$pkg/"
    cp -R LICENSES "dist/stage/$pkg/"
    tar --owner=0 --group=0 --numeric-owner -czf "dist/$pkg.tar.gz" -C dist/stage "$pkg"
done
rm -r dist/stage
(cd dist && sha256sum execonnect-* >checksums.txt)

gh release create "$TAG" --title "$TAG" \
    --notes "$V, built from $(git rev-parse --short HEAD)." \
    dist/execonnect-* dist/checksums.txt
