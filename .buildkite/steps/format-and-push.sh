#!/usr/bin/env bash
# format-and-push.sh — Run formatters and report whether changes are needed.
# Sets Buildkite metadata "needs_formatting" to "true" or "false".
# Does NOT commit; the rebase-and-push step handles that.
set -euo pipefail

export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"

# Get node/pnpm for shelley/ui formatting
source .buildkite/steps/setup-shelley-deps.sh

./bin/run_formatters.sh

if ! git diff --quiet; then
    echo "Formatting changes detected"
    buildkite-agent meta-data set needs_formatting true
    git diff --stat
    git checkout -- .
else
    echo "No formatting changes needed"
    buildkite-agent meta-data set needs_formatting false
fi

# Pre-generate GitHub App token for the push step on queue branches.
# This hides the ~1.3s token generation latency from the critical path.
if [[ "${BUILDKITE_BRANCH:-}" == kite-queue-* ]]; then
    echo "--- :key: Pre-generate GitHub App token"
    token=$(python3 .buildkite/steps/github_token.py)
    buildkite-agent meta-data set github_app_token "$token"
    echo "Token saved to metadata."
fi
