#!/bin/bash
#
# Build libghostty-vt for the iOS app.
#
# This script is meant to be called as an Xcode "Run Script" build phase,
# OR run manually before building in Xcode.
#
# Prerequisites:
#   - Zig 0.15.2+ on PATH
#   - Ghostty source tree at ../deps/ghostty relative to this script, or set
#     GHOSTTY_SRC to another checkout location.
#
# The script pins Ghostty to a tested revision so upstream API churn
# doesn't silently break the app.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEFAULT_GHOSTTY_SRC="$SCRIPT_DIR/../deps/ghostty"
GHOSTTY_SRC="${GHOSTTY_SRC:-$DEFAULT_GHOSTTY_SRC}"
GHOSTTY_REPO_URL="${GHOSTTY_REPO_URL:-https://github.com/ghostty-org/ghostty.git}"
GHOSTTY_REV="${GHOSTTY_REV:-48d3e972d839999745368b156df396d9512fd17b}"

OUT_DIR="$SCRIPT_DIR/GhosttyVT"
LIB_DIR="$OUT_DIR/lib"
INC_DIR="$OUT_DIR/include"
TMP_DIR="$OUT_DIR/.build"

clone_ghostty_if_needed() {
    if [ -e "$GHOSTTY_SRC" ]; then
        return
    fi

    echo "Cloning Ghostty into $GHOSTTY_SRC..."
    mkdir -p "$(dirname "$GHOSTTY_SRC")"
    git clone "$GHOSTTY_REPO_URL" "$GHOSTTY_SRC"
}

pin_ghostty_revision() {
    if [ ! -d "$GHOSTTY_SRC/.git" ]; then
        echo "error: $GHOSTTY_SRC exists but is not a git checkout"
        exit 1
    fi

    if [ -n "$(git -C "$GHOSTTY_SRC" status --short)" ]; then
        echo "error: $GHOSTTY_SRC has local changes; clean it before building"
        exit 1
    fi

    if ! git -C "$GHOSTTY_SRC" cat-file -e "$GHOSTTY_REV^{commit}" 2>/dev/null; then
        echo "Fetching Ghostty history to find pinned revision $GHOSTTY_REV..."
        git -C "$GHOSTTY_SRC" fetch --tags origin
    fi

    if ! git -C "$GHOSTTY_SRC" cat-file -e "$GHOSTTY_REV^{commit}" 2>/dev/null; then
        echo "error: pinned Ghostty revision $GHOSTTY_REV not found in $GHOSTTY_SRC"
        exit 1
    fi

    if [ "$(git -C "$GHOSTTY_SRC" rev-parse HEAD)" != "$GHOSTTY_REV" ]; then
        echo "Checking out pinned Ghostty revision $GHOSTTY_REV..."
        git -C "$GHOSTTY_SRC" checkout --detach "$GHOSTTY_REV" >/dev/null
    fi
}

build_target() {
    local zig_target="$1"
    local output_lib="$2"

    echo "Building libghostty-vt for $zig_target..."
    rm -rf "$GHOSTTY_SRC/zig-out"
    (
        cd "$GHOSTTY_SRC"
        zig build \
            -Demit-lib-vt=true \
            -Dtarget="$zig_target" \
            -Doptimize=ReleaseFast \
            -Dsimd=false
    )

    cp -f "$GHOSTTY_SRC/zig-out/lib/libghostty-vt.a" "$output_lib"
}

clone_ghostty_if_needed

if [ ! -f "$GHOSTTY_SRC/build.zig" ]; then
    echo "error: Ghostty source not found at $GHOSTTY_SRC"
    exit 1
fi

pin_ghostty_revision
echo "Using ghostty source at: $GHOSTTY_SRC"
echo "Pinned Ghostty revision: $GHOSTTY_REV"

declare -a ZIG_TARGETS=()
if [ -n "${PLATFORM_NAME:-}" ]; then
    case "$PLATFORM_NAME" in
    iphoneos)
        ZIG_TARGETS=("aarch64-ios")
        ;;
    iphonesimulator)
        if [[ " ${ARCHS:-arm64 x86_64} " == *" arm64 "* ]]; then
            ZIG_TARGETS+=("aarch64-ios-simulator")
        fi
        if [[ " ${ARCHS:-arm64 x86_64} " == *" x86_64 "* ]]; then
            ZIG_TARGETS+=("x86_64-ios-simulator")
        fi
        if [ "${#ZIG_TARGETS[@]}" -eq 0 ]; then
            ZIG_TARGETS=("aarch64-ios-simulator")
        fi
        ;;
    *)
        echo "error: unknown platform $PLATFORM_NAME"
        exit 1
        ;;
    esac
else
    # Manual build defaults to device.
    ZIG_TARGETS=("aarch64-ios")
fi

rm -rf "$TMP_DIR" "$LIB_DIR/libghostty-vt.a" "$INC_DIR/ghostty" "$GHOSTTY_SRC/zig-out" "$GHOSTTY_SRC/.zig-cache"
mkdir -p "$TMP_DIR" "$LIB_DIR" "$INC_DIR"

declare -a BUILT_LIBS=()
for zig_target in "${ZIG_TARGETS[@]}"; do
    target_lib="$TMP_DIR/libghostty-vt-$zig_target.a"
    build_target "$zig_target" "$target_lib"
    BUILT_LIBS+=("$target_lib")
done

if [ "${#BUILT_LIBS[@]}" -eq 1 ]; then
    cp -f "${BUILT_LIBS[0]}" "$LIB_DIR/libghostty-vt.a"
else
    lipo -create "${BUILT_LIBS[@]}" -output "$LIB_DIR/libghostty-vt.a"
fi

cp -Rf "$GHOSTTY_SRC/zig-out/include/ghostty" "$INC_DIR/"
rm -rf "$TMP_DIR"

echo "libghostty-vt built successfully -> $OUT_DIR"
lipo -info "$LIB_DIR/libghostty-vt.a"
