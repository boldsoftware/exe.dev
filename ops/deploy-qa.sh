#!/bin/bash
set -euo pipefail

API_ENDPOINT="https://exed-02.crocodile-vector.ts.net/debug/gitsha"

echo "🕸️  checking what is deployed..."

if ! DEPLOYED_SHA=$(curl -s "${API_ENDPOINT}"); then
    echo "😞 could not get deployed SHA (curl failed)"
    exit 1
fi

if [ -z "${DEPLOYED_SHA}" ]; then
    echo "😞 could not get deployed SHA (empty response)"
    exit 1
fi

if ! git rev-parse --quiet --verify "${DEPLOYED_SHA}" >/dev/null; then
    echo "😞 could not get deployed SHA (invalid SHA)"
    echo "  ${DEPLOYED_SHA}"
    exit 1
fi

CURRENT_SHA=$(git rev-parse HEAD)

if [ "${DEPLOYED_SHA}" = "${CURRENT_SHA}" ]; then
    echo "✅ already deployed: ${DEPLOYED_SHA}"
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
I'm about to deploy HEAD to production. ${DEPLOYED_SHA} is currently deployed.

Please inspect all the intervening commits. (You may ignore any devlog commits.)
You have two goals:

- Look for significant issues that we should investigate before deploying.
- Make a list of important things to test manually in production after deployment.

Read deeply. Understand the codebase and the changes, and think everything through carefully.
EOF
