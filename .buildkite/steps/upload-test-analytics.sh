#!/usr/bin/env bash
set -euo pipefail

# Upload JUnit XML test results to Buildkite Test Analytics.
# Usage: upload-test-analytics.sh <junit-xml-file> [<junit-xml-file> ...]
#
# Requires BUILDKITE=true and the TEST_ANALYTICS_TOKEN secret.
# Silently skips if not running in Buildkite CI or token is unavailable.

if [ "${BUILDKITE:-}" != "true" ]; then
    exit 0
fi

TOKEN=$(buildkite-agent secret get TEST_ANALYTICS_TOKEN 2>/dev/null) || true
if [ -z "$TOKEN" ]; then
    echo "WARNING: TEST_ANALYTICS_TOKEN secret not available, skipping test analytics upload" >&2
    exit 0
fi

for file in "$@"; do
    if [ ! -f "$file" ]; then
        echo "WARNING: $file not found, skipping" >&2
        continue
    fi
    echo "Uploading $file to Buildkite Test Analytics..." >&2
    curl \
        -X POST \
        --fail-with-body \
        -H "Authorization: Token token=\"$TOKEN\"" \
        -F "data=@$file" \
        -F "format=junit" \
        -F "run_env[CI]=buildkite" \
        -F "run_env[key]=$BUILDKITE_BUILD_ID" \
        -F "run_env[number]=$BUILDKITE_BUILD_NUMBER" \
        -F "run_env[job_id]=$BUILDKITE_JOB_ID" \
        -F "run_env[branch]=$BUILDKITE_BRANCH" \
        -F "run_env[commit_sha]=$BUILDKITE_COMMIT" \
        -F "run_env[message]=$BUILDKITE_MESSAGE" \
        -F "run_env[url]=$BUILDKITE_BUILD_URL" \
        https://analytics-api.buildkite.com/v1/uploads \
        >&2 || echo "WARNING: Failed to upload $file to Test Analytics" >&2
done
