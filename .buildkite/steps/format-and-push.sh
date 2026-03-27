#!/usr/bin/env bash
# format-and-push.sh — Run formatters and commit fixes if needed.
# The rebase-and-push step will handle pushing.
set -euo pipefail

export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"

# Get node/pnpm for shelley/ui formatting
source .buildkite/steps/setup-shelley-deps.sh

./bin/run_formatters.sh

if ! git diff --quiet; then
    git config --global user.name "Auto-formatter"
    git config --global user.email "bot@exe.dev"
    git add .
    git commit -m "all: fix formatting"
else
    echo "No formatting changes needed"
fi
