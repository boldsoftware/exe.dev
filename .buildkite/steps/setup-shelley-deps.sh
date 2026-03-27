#!/usr/bin/env bash
set -euo pipefail
trap 'echo "Error at line $LINENO"' ERR

# Shared setup for shelley CI steps.
# Installs Node.js (via uvx nodeenv), pnpm, and headless-shell.
# All cached under $HOME/.cache/kite/ for reuse across builds.
#
# After sourcing, PATH includes node, pnpm, headless-shell, go.

KITE_CACHE="$HOME/.cache/kite"
NODE_DIR="$KITE_CACHE/node"
HEADLESS_DIR="$KITE_CACHE/headless-shell"

export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"

# --- uv (needed for nodeenv) ---
if ! command -v uv >/dev/null 2>&1; then
  curl -LsSf https://astral.sh/uv/install.sh | sh
fi

# --- Node.js via nodeenv ---
if [ ! -x "$NODE_DIR/bin/node" ]; then
  echo "Installing Node.js LTS via nodeenv..."
  rm -rf "$NODE_DIR"
  uvx nodeenv --node=lts "$NODE_DIR"
fi
export PATH="$NODE_DIR/bin:$PATH"
echo "node $(node --version)"

# --- pnpm ---
if ! command -v pnpm >/dev/null 2>&1; then
  npm install -g pnpm@10
fi
export PNPM_HOME="$KITE_CACHE/pnpm-global"
export PATH="$PNPM_HOME:$PATH"
pnpm config set store-dir "$KITE_CACHE/pnpm-store"
echo "pnpm $(pnpm --version)"

# --- headless-shell (for chromedp Go tests) ---
if [ ! -x "$HEADLESS_DIR/headless-shell" ]; then
  echo "Extracting headless-shell from chromedp/headless-shell:stable..."
  rm -rf "$HEADLESS_DIR"
  mkdir -p "$HEADLESS_DIR"
  CID=$(docker create chromedp/headless-shell:stable /bin/true)
  docker cp "$CID:/headless-shell/." "$HEADLESS_DIR/"
  docker rm "$CID" >/dev/null
  chmod -R a+rx "$HEADLESS_DIR"
fi
export PATH="$HEADLESS_DIR:$PATH"

# --- git identity (needed by some shelley tests) ---
git config --global user.email "ci@exe.dev" 2>/dev/null || true
git config --global user.name "exe CI" 2>/dev/null || true
