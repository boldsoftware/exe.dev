#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Script to run formatters for both Go and frontend files
# Usage: bin/run_formatters.sh [check]
# If 'check' is provided, only checks formatting without making changes

CHECK_MODE=false
if [ "${1:-}" = "check" ]; then
	CHECK_MODE=true
	echo "Running in check mode (formatting will not be modified)"
else
	echo "Running in fix mode (formatting will be modified)"
fi

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
if [ "$CHECK_MODE" = true ]; then
	shfmt -d $(git ls-files -- "*.sh" | grep -v -E '^(tini|sshpiper)')
else
	shfmt -w $(git ls-files -- "*.sh" | grep -v -E '^(tini|sshpiper)')
fi

# Go formatting with gofumpt
echo "Checking Go formatting..."
if [ "$CHECK_MODE" = true ]; then
	# In check mode, we want to display the files that need formatting and exit with error if any
	FILES_TO_FORMAT=$(find . -name "*.go" -exec gofumpt -extra -l {} +)
	if [ -n "$FILES_TO_FORMAT" ]; then
		echo "The following Go files need formatting:"
		echo "$FILES_TO_FORMAT"
		exit 1
	else
		echo "✓ Go formatting check passed"
	fi
else
	# In fix mode, we apply the formatting
	echo "Formatting Go code with gofumpt..."
	# I tried all three approaches here, and xargs with parallelism and batches
	# of 20 files seemed faster than the other approaches.
	# time find . -name "*.go" -not -path "./sshpiper/*" -exec gofumpt -extra -w {} +
	# time gofumpt -extra -w $(git ls-files -- "*.go" | grep -v -E '^sshpiper')
	git ls-files -- "*.go" | grep -v -E '^sshpiper' | xargs -P $(nproc) -n 20 gofumpt -extra -w
	echo "✓ Go code formatted"
fi
echo ""

# Frontend formatting with Prettier (in shelley/ui directory)
if [ -d "shelley/ui" ]; then
	echo "Checking shelley/ui formatting..."
	cd shelley/ui
	if [ "$CHECK_MODE" = true ]; then
		echo "Checking TypeScript/JavaScript formatting with prettier"
		if ! npx -y prettier@3.6.2 --check 'src/**/*.{ts,tsx,js,jsx,json,css,html}'; then
			echo "shelley/ui files need formatting"
			cd ../..
			exit 1
		fi
		echo "✓ shelley/ui formatting check passed"
	else
		echo "Formatting TypeScript/JavaScript code with prettier..."
		npx -y prettier@3.6.2 --write 'src/**/*.{ts,tsx,js,jsx,json,css,html}'
		echo "✓ shelley/ui code formatted"
	fi
	cd ../..
	echo ""
fi

if [ "$CHECK_MODE" = true ]; then
	echo "All formatting checks passed!"
else
	echo "All formatting complete!"
fi
