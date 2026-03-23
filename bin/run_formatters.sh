#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Script to run formatters for Go, frontend, and shell scripts.

# Usage: scripts/run_formatters.sh
#   This edits your tree in place.

# Shell formatting
echo "Checking Shell formatting..."
go tool shfmt -i 4 -w $(git ls-files -- "*.sh" | grep -v -E '^(tini|deps/sshpiper)')

# Go formatting with gofumpt
echo "Checking Go formatting..."
echo "Formatting Go code with gofumpt..."
# I tried all three approaches here, and xargs with parallelism and batches
# of 20 files seemed faster than the other approaches.
# time find . -name "*.go" -not -path "./deps/sshpiper/*" -exec gofumpt -extra -w {} +
# time gofumpt -extra -w $(git ls-files -- "*.go" | grep -v -E '^deps/sshpiper')
# Exclude generated files since they may have different formatting from code generators
git ls-files -- "*.go" | grep -v -E '^deps/sshpiper' | grep -v -E '(_string|\.gen|\.pb)\.go$' | grep -v -E '^(shelley/)?db/generated/' | xargs -P $(nproc) -n 20 go tool gofumpt -extra -w
echo "✓ Go code formatted"

echo "Checking shelley/ui formatting..."

# Bootstrap node/pnpm if not available (common on exe.dev VMs)
if ! command -v node >/dev/null 2>&1 || ! command -v pnpm >/dev/null 2>&1; then
    NODE_DIR="${HOME}/.local/share/nodeenv"
    if [ ! -x "${NODE_DIR}/bin/node" ]; then
        echo "node/pnpm not found, installing via uvx nodeenv..."
        uvx nodeenv --node=lts --prebuilt "${NODE_DIR}"
    fi
    export PATH="${NODE_DIR}/bin:${PATH}"
    if ! command -v pnpm >/dev/null 2>&1; then
        echo "Installing pnpm..."
        npm install -g pnpm
    fi
fi

cd shelley/ui
if ! pnpm exec prettier --version >/dev/null 2>&1; then
    echo "prettier not found, running pnpm install..."
    pnpm install
fi
echo "Formatting TypeScript/JavaScript code with prettier..."
pnpm exec prettier --log-level warn --write 'src/**/*.{ts,tsx,js,jsx,json,css,html}'
echo "✓ shelley/ui code formatted"
cd ../..

echo "All formatting complete!"
