#!/bin/bash
set -euo pipefail

EXED_ENDPOINT="https://exed-02.crocodile-vector.ts.net/debug/gitsha"

echo "🕸️  checking what is deployed..."

if ! EXED_SHA=$(curl -s "${EXED_ENDPOINT}"); then
    echo "😞 could not get exed SHA (curl failed)"
    exit 1
fi

if [ -z "${EXED_SHA}" ]; then
    echo "😞 could not get exed SHA (empty response from ${EXED_ENDPOINT})"
    exit 1
fi

if ! git rev-parse --quiet --verify "${EXED_SHA}" >/dev/null; then
    echo "😞 exed SHA is not a valid commit: ${EXED_SHA}"
    echo "  (from ${EXED_ENDPOINT})"
    exit 1
fi

# Get one exelet SHA for deployment order consideration
EXELET_HOST=$(ssh exe.dev exelets --json | jq -r '.exelets[0].host')
if [ -n "$EXELET_HOST" ]; then
    EXELET_SHA=$(curl -s "http://${EXELET_HOST}.crocodile-vector.ts.net:9081/debug/gitsha")
fi

CURRENT_SHA=$(git rev-parse HEAD)

if [ "${EXED_SHA}" = "${CURRENT_SHA}" ]; then
    echo "✅ exed already deployed: ${EXED_SHA}"
    exit 0
fi

if ! command -v claude >/dev/null 2>&1; then
    echo "😞 claude CLI not found (install claude before running deploy-qa)"
    exit 1
fi

echo
echo "running claude. this will take a while..."
echo

claude --model claude-opus-4-5-20251101 --dangerously-skip-permissions -p - <<EOF
I'm about to deploy exed to production, from this worktree.
Exed ${EXED_SHA} is currently deployed.
Exelet ${EXELET_SHA:-unknown} is currently deployed.

Please inspect all the intervening commits.
You may ignore any commits that do not alter prod, e.g. changes that only touch the devlog, CI, or tests.

You have three goals:

- Look for significant issues that we should investigate before deploying.
- Consider deployment order if there are dependencies between exed and exelet.
- Make a list of important things to test manually in production after deployment.

Read deeply. Understand the codebase and the changes, and think everything through carefully.
EOF
