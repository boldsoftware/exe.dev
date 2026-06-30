#!/bin/sh
# Reproducible static build of exe-scroll: uses mise to fetch the pinned Zig
# toolchain (versioned + checksummed in mise.toml / mise.lock; the tarball is
# verified against the Zig Software Foundation's signing key), clones Ghostty at
# a pinned revision, and `zig build`s exe-scroll.zig + ghostty-vt into one fully
# static musl binary (no dynamic interpreter at all).
#
# Usage: build-static.sh <target_arch>   (arm64 or amd64)
set -e

ARCH="$1"
if [ -z "$ARCH" ]; then
    echo "Usage: $0 <target_arch>  (arm64 or amd64)" >&2
    exit 1
fi

GHOSTTY_REV="${GHOSTTY_REV:-48d3e972d839999745368b156df396d9512fd17b}"
GHOSTTY_REPO_URL="${GHOSTTY_REPO_URL:-https://github.com/ghostty-org/ghostty.git}"

# Directory containing build.zig (this script's directory by default).
SRC_DIR="${SRC_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)}"

case "$ARCH" in
amd64 | x86_64)
    ZIG_TARGET="x86_64-linux-musl"
    EXPECT_UNAME="x86_64"
    ;;
arm64 | aarch64)
    ZIG_TARGET="aarch64-linux-musl"
    EXPECT_UNAME="aarch64"
    ;;
*)
    echo "build-static.sh: unsupported target arch '$ARCH'" >&2
    exit 1
    ;;
esac

if [ "$(uname -m)" != "$EXPECT_UNAME" ]; then
    echo "build-static.sh: target $ARCH expects uname -m=$EXPECT_UNAME but got" \
        "$(uname -m); run under the target platform (buildx/qemu)." >&2
    exit 1
fi

echo "Building exe-scroll for $ARCH (zig target $ZIG_TARGET)..."

# --- Toolchain (zig) via mise -------------------------------------------
# mise.toml pins the Zig version; mise.lock pins per-platform checksums, so the
# toolchain itself is reproducible. We install mise on demand if it isn't
# already present, then let it fetch the exact toolchain. `mise exec` runs the
# build with that zig on PATH.
#
# Note: the on-demand bootstrap below pulls the latest mise (not version-pinned).
# For a fully pinned chain, preinstall a known mise version yourself.
if ! command -v mise >/dev/null 2>&1; then
    echo "Installing mise..."
    curl -fsSL https://mise.run | sh
    export PATH="$HOME/.local/bin:$PATH"
fi
mise trust "$SRC_DIR/mise.toml"
mise install --cd "$SRC_DIR"

# --- Fetch ghostty at the pinned commit ---------------------------------
GHOSTTY_SRC="${GHOSTTY_SRC:-/src/ghostty}"
if [ ! -d "$GHOSTTY_SRC/.git" ]; then
    echo "Cloning ghostty..."
    git clone --filter=tree:0 "$GHOSTTY_REPO_URL" "$GHOSTTY_SRC"
fi
if ! git -C "$GHOSTTY_SRC" cat-file -e "${GHOSTTY_REV}^{commit}" 2>/dev/null; then
    git -C "$GHOSTTY_SRC" fetch --filter=tree:0 origin "$GHOSTTY_REV"
fi
git -C "$GHOSTTY_SRC" checkout --detach "$GHOSTTY_REV"

ln -sfn "$GHOSTTY_SRC" "$SRC_DIR/ghostty-src"

# --- Build --------------------------------------------------------------
(
    cd "$SRC_DIR"
    rm -rf .zig-cache zig-out
    mise exec -- zig build -Dtarget="$ZIG_TARGET" -Doptimize=ReleaseFast
)

OUT="$SRC_DIR/zig-out/bin/exe-scroll"
if [ ! -f "$OUT" ]; then
    echo "build-static.sh: exe-scroll binary not produced" >&2
    exit 1
fi
strip --strip-all --remove-section=.comment --remove-section=.note "$OUT" 2>/dev/null || true

echo "exe-scroll (static musl) built successfully for $ARCH: $OUT"
