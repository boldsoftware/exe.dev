#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Script to run formatters for Go, frontend, and shell scripts.

# Usage: bin/run_formatters.sh
#   This edits your tree in place.

if ! command -v gofumpt &>/dev/null; then
	echo "Error: gofumpt not found. Install it with:"
	echo "  go install mvdan.cc/gofumpt@v0.9.2"
	exit 1
fi

if ! command -v shfmt &>/dev/null; then
	echo "Error: shfmt not found. Install it with:"
	echo "  go install mvdan.cc/sh/v3/cmd/shfmt@v3.12.0"
	exit 1
fi

# Shell formatting
echo "Checking Shell formatting..."
shfmt -w $(git ls-files -- "*.sh" | grep -v -E '^(tini|sshpiper)')

# Go formatting with gofumpt
echo "Checking Go formatting..."
echo "Formatting Go code with gofumpt..."
# I tried all three approaches here, and xargs with parallelism and batches
# of 20 files seemed faster than the other approaches.
# time find . -name "*.go" -not -path "./sshpiper/*" -exec gofumpt -extra -w {} +
# time gofumpt -extra -w $(git ls-files -- "*.go" | grep -v -E '^sshpiper')
git ls-files -- "*.go" | grep -v -E '^sshpiper' | xargs -P $(nproc) -n 20 gofumpt -extra -w
echo "✓ Go code formatted"

echo "Checking shelley/ui formatting..."
cd shelley/ui
echo "Formatting TypeScript/JavaScript code with prettier..."
npx -y prettier@3.6.2 --write 'src/**/*.{ts,tsx,js,jsx,json,css,html}'
echo "✓ shelley/ui code formatted"
cd ../..

echo "All formatting complete!"
