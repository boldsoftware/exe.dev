#!/bin/bash
#
# Build libghostty-vt for the iOS app.
#
# This script is meant to be called as an Xcode "Run Script" build phase,
# OR run manually before building in Xcode.
#
# Prerequisites:
#   - Zig 0.15.2+ on PATH
#   - Ghostty source tree cloned (set GHOSTTY_SRC env var, or place at
#     the same level as the exe repo: ../ghostty relative to repo root)
#
# The built library is placed in iOS/GhosttyVT/lib/ and headers in
# iOS/GhosttyVT/include/ so Xcode can find them.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Locate ghostty source.
GHOSTTY_SRC="${GHOSTTY_SRC:-}"
if [ -z "$GHOSTTY_SRC" ]; then
    # Try common locations
    for candidate in \
        "$SCRIPT_DIR/../../../ghostty" \
        "$SCRIPT_DIR/../../ghostty" \
        "$HOME/ghostty" \
        "$HOME/src/ghostty"; do
        if [ -f "$candidate/build.zig" ]; then
            GHOSTTY_SRC="$candidate"
            break
        fi
    done
fi

if [ -z "$GHOSTTY_SRC" ] || [ ! -f "$GHOSTTY_SRC/build.zig" ]; then
    echo "error: Ghostty source not found. Set GHOSTTY_SRC or clone ghostty next to the exe repo."
    echo "  git clone https://github.com/ghostty-org/ghostty.git"
    exit 1
fi

echo "Using ghostty source at: $GHOSTTY_SRC"

# Determine target based on Xcode build settings (if running from Xcode)
# or default to aarch64-ios for manual builds.
if [ -n "${PLATFORM_NAME:-}" ]; then
    case "$PLATFORM_NAME" in
        iphoneos)
            ZIG_TARGET="aarch64-ios"
            ;;
        iphonesimulator)
            case "${ARCHS:-arm64}" in
                *arm64*) ZIG_TARGET="aarch64-ios-simulator" ;;
                *x86_64*) ZIG_TARGET="x86_64-ios-simulator" ;;
                *) ZIG_TARGET="aarch64-ios-simulator" ;;
            esac
            ;;
        *)
            echo "error: Unknown platform $PLATFORM_NAME"
            exit 1
            ;;
    esac
else
    # Manual build — default to device
    ZIG_TARGET="aarch64-ios"
fi

OUT_DIR="$SCRIPT_DIR/GhosttyVT"
LIB_DIR="$OUT_DIR/lib"
INC_DIR="$OUT_DIR/include"

# Check if we already have a built library (skip rebuild if up to date)
if [ -f "$LIB_DIR/libghostty-vt.a" ]; then
    LIB_AGE=$(stat -f %m "$LIB_DIR/libghostty-vt.a" 2>/dev/null || stat -c %Y "$LIB_DIR/libghostty-vt.a" 2>/dev/null || echo 0)
    BUILD_ZIG_AGE=$(stat -f %m "$GHOSTTY_SRC/build.zig" 2>/dev/null || stat -c %Y "$GHOSTTY_SRC/build.zig" 2>/dev/null || echo 1)
    if [ "$LIB_AGE" -gt "$BUILD_ZIG_AGE" ]; then
        echo "libghostty-vt.a is up to date, skipping build"
        exit 0
    fi
fi

echo "Building libghostty-vt for $ZIG_TARGET..."

cd "$GHOSTTY_SRC"
zig build \
    -Demit-lib-vt=true \
    -Dtarget="$ZIG_TARGET" \
    -Doptimize=ReleaseFast \
    -Dsimd=false

# Copy outputs
mkdir -p "$LIB_DIR" "$INC_DIR"
cp -f "$GHOSTTY_SRC/zig-out/lib/libghostty-vt.a" "$LIB_DIR/"
cp -rf "$GHOSTTY_SRC/zig-out/include/ghostty" "$INC_DIR/"

echo "libghostty-vt built successfully for $ZIG_TARGET -> $OUT_DIR"
