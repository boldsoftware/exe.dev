#!/bin/sh
# Build ghostty-web with our patches: the TypeScript library plus ghostty-vt.wasm.
#
# Clones coder/ghostty-web and ghostty-org/ghostty at pinned revisions into
# gitignored caches here, applies patches/*.patch to ghostty-web, applies
# ghostty-web's WASM API patch to ghostty, and builds the WASM with the Zig
# pinned in mise.toml. Writes OUT_DIR (default ../web/ghostty-web):
#   lib/            the library source, for the app's Vite build to compile
#   ghostty-vt.wasm
#   LICENSE
#
# Usage: build.sh
set -e

SRC_DIR="${SRC_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)}"
OUT_DIR="${OUT_DIR:-$SRC_DIR/../web/ghostty-web}"

GHOSTTY_WEB_REV=6a1a50df5b4f6b34d1b1de10fad3a0fc811bfbc0
GHOSTTY_WEB_REPO_URL="${GHOSTTY_WEB_REPO_URL:-https://github.com/coder/ghostty-web.git}"
# The ghostty submodule commit that GHOSTTY_WEB_REV pins, and the Zig it builds with.
GHOSTTY_REV=5714ed07a1012573261b7b7e3ed2add9c1504496
GHOSTTY_REPO_URL="${GHOSTTY_REPO_URL:-https://github.com/ghostty-org/ghostty.git}"

# Zig via mise, installed on demand, as oss/exe-scroll/build-static.sh does.
# No lockfile: mise verifies Zig tarballs against the Zig Software
# Foundation's signing key, and with a lockfile on, a mise older or newer
# than the one that wrote ../mise.lock rewrites it in passing.
export MISE_LOCKFILE=false
if ! command -v mise >/dev/null 2>&1; then
    curl -fsSL https://mise.run | sh
    export PATH="$HOME/.local/bin:$PATH"
fi
# mise also reads ../mise.toml, exe-tty's, and refuses to run while any
# config it reads is untrusted.
mise trust --quiet "$SRC_DIR/mise.toml"
mise trust --quiet "$SRC_DIR/../mise.toml"
mise install --quiet --cd "$SRC_DIR" zig
ZIG_BIN="$(mise bin-paths --cd "$SRC_DIR" zig)"

# Clone a repo at a revision into a cache dir, discarding any local changes.
checkout() {
    url=$1 rev=$2 dir=$3
    if [ ! -d "$dir/.git" ]; then
        git clone --quiet --filter=tree:0 "$url" "$dir"
    fi
    if ! git -C "$dir" cat-file -e "$rev^{commit}" 2>/dev/null; then
        git -C "$dir" fetch --quiet --filter=tree:0 origin "$rev"
    fi
    git -C "$dir" checkout --quiet --detach "$rev"
    git -C "$dir" reset --quiet --hard
    git -C "$dir" clean --quiet -fdx -e zig-out -e .zig-cache
}

# Private clones, not exe-scroll's GHOSTTY_SRC: this one gets a different
# revision and a patched working tree, and the two builds can run at once.
GHOSTTY_WEB_SRC="$SRC_DIR/.ghostty-web"
GHOSTTY_SRC="$SRC_DIR/.ghostty"

checkout "$GHOSTTY_WEB_REPO_URL" "$GHOSTTY_WEB_REV" "$GHOSTTY_WEB_SRC"

# Apply our patches. One that upstream has since merged applies in reverse;
# fail so it gets deleted rather than silently skipped.
for p in "$SRC_DIR"/patches/*.patch; do
    if git -C "$GHOSTTY_WEB_SRC" apply --reverse --check "$p" 2>/dev/null; then
        echo "build.sh: ${p##*/} is already in ghostty-web $GHOSTTY_WEB_REV; delete it" >&2
        exit 1
    fi
    git -C "$GHOSTTY_WEB_SRC" apply --whitespace=nowarn "$p"
done

checkout "$GHOSTTY_REPO_URL" "$GHOSTTY_REV" "$GHOSTTY_SRC"
git -C "$GHOSTTY_SRC" apply --whitespace=nowarn "$GHOSTTY_WEB_SRC/patches/ghostty-wasm-api.patch"

# The same flags as ghostty-web's scripts/build-wasm.sh, so the unpatched
# build is byte-identical to the npm package's wasm. Fetching the Zig
# dependencies into Zig's global cache first, with retries: the downloads
# fail now and then on a cold machine, and zig build does not retry.
(
    cd "$GHOSTTY_SRC"
    export PATH="$ZIG_BIN:$PATH"
    attempt=1
    until zig build lib-vt -Dtarget=wasm32-freestanding --fetch; do
        if [ "$attempt" -ge 3 ]; then
            echo "build.sh: fetching Zig dependencies failed" >&2
            exit 1
        fi
        attempt=$((attempt + 1))
        sleep 5
    done
    zig build lib-vt -Dtarget=wasm32-freestanding -Doptimize=ReleaseSmall
)

tmp="$OUT_DIR.tmp.$$"
rm -rf "$tmp"
mkdir -p "$tmp"
cp -R "$GHOSTTY_WEB_SRC/lib" "$tmp/lib"
find "$tmp/lib" \( -name '*.test.ts' -o -name test-helpers.ts \) -delete
cp "$GHOSTTY_SRC/zig-out/bin/ghostty-vt.wasm" "$tmp/ghostty-vt.wasm"
cp "$GHOSTTY_WEB_SRC/LICENSE" "$tmp/LICENSE"
rm -rf "$OUT_DIR"
mv "$tmp" "$OUT_DIR"
