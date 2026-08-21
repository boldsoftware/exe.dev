#!/bin/sh
# Build the released exe-scroll source for macOS on a GitHub macOS runner.
set -e

ARCH="$1"
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SRC="$ROOT/exe-scroll"
GHOSTTY_REV="${GHOSTTY_REV:-48d3e972d839999745368b156df396d9512fd17b}"
GHOSTTY_SRC="${GHOSTTY_SRC:-$HOME/.cache/exe-scroll-ci/ghostty-src}"
OUT_DIR="${OUT_DIR:-$SRC/zig-out-$ARCH}"

case "$ARCH" in
amd64 | x86_64) TARGET=x86_64-macos ;;
arm64 | aarch64) TARGET=aarch64-macos ;;
*)
    echo "unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

if ! command -v mise >/dev/null 2>&1; then
    curl -fsSL https://mise.run | sh
    PATH="$HOME/.local/bin:$PATH"
    export PATH
fi
mise trust "$SRC/mise.toml"
mise install --cd "$SRC"

if [ ! -d "$GHOSTTY_SRC/.git" ]; then
    mkdir -p "$(dirname "$GHOSTTY_SRC")"
    git clone --filter=tree:0 https://github.com/ghostty-org/ghostty.git "$GHOSTTY_SRC"
fi
git -C "$GHOSTTY_SRC" cat-file -e "${GHOSTTY_REV}^{commit}" 2>/dev/null ||
    git -C "$GHOSTTY_SRC" fetch --filter=tree:0 origin "$GHOSTTY_REV"
git -C "$GHOSTTY_SRC" checkout --quiet --detach "$GHOSTTY_REV"
ln -sfn "$GHOSTTY_SRC" "$SRC/ghostty-src"

(
    cd "$SRC"
    mise exec -- zig build \
        -Dtarget="$TARGET" -Doptimize=ReleaseFast -Dstrip=true \
        --cache-dir ".zig-cache/$TARGET" \
        -p "$OUT_DIR"
)

test -x "$OUT_DIR/bin/exe-scroll"
