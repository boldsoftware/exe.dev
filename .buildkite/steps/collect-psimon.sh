#!/usr/bin/env bash
# collect-psimon.sh — Query the psimon daemon for pressure data covering this build.
# Downloads the Vega-Lite HTML report and saves it as an artifact.
set -euo pipefail
trap 'echo Error in $0 at line $LINENO' ERR

PSIMON_URL="http://localhost:9101"

# Check if psimon is reachable.
if ! curl -sf "${PSIMON_URL}/health" >/dev/null 2>&1; then
    echo "psimon not reachable at ${PSIMON_URL}, skipping" >&2
    exit 0
fi

# Get build start time from Buildkite metadata, or fall back to 1 hour ago.
START_TS=""
if [ -n "${BUILDKITE_BUILD_CREATED_AT:-}" ]; then
    START_TS=$(date -d "${BUILDKITE_BUILD_CREATED_AT}" +%s 2>/dev/null || true)
fi
if [ -z "${START_TS}" ] && [ -n "${BUILDKITE_BUILD_ID:-}" ]; then
    # Try buildkite-agent meta-data for start time stored by pipeline generation.
    START_TS=$(buildkite-agent meta-data get psimon-start 2>/dev/null || true)
fi
if [ -z "${START_TS}" ]; then
    START_TS=$(($(date +%s) - 3600))
    echo "WARNING: Could not determine build start time, using now-1h" >&2
fi
END_TS=$(date +%s)

echo "Querying psimon: start=${START_TS} end=${END_TS}" >&2
curl -sf "${PSIMON_URL}/query?start=${START_TS}&end=${END_TS}" -o psimon-pressure.html

if [ -s psimon-pressure.html ]; then
    echo "Saved psimon-pressure.html ($(wc -c <psimon-pressure.html) bytes)" >&2
else
    echo "WARNING: psimon returned empty response" >&2
    rm -f psimon-pressure.html
fi
