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

# Check if gofumpt is installed
if ! command -v gofumpt &> /dev/null; then
    echo "Error: gofumpt not found. Install it with:"
    echo "  go install mvdan.cc/gofumpt@v0.9.0"
    exit 1
fi

# Go formatting with gofumpt
echo "Checking Go formatting..."
if [ "$CHECK_MODE" = true ]; then
  # In check mode, we want to display the files that need formatting and exit with error if any
  FILES_TO_FORMAT=$(find . -name "*.go" -exec gofumpt -l {} +)
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
  find . -name "*.go" -exec gofumpt -w {} +
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
