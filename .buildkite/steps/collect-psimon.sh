#!/usr/bin/env bash
# collect-psimon.sh — Query the psimon daemon for pressure data covering this build.
# Downloads the Vega-Lite HTML report and saves it as an artifact. Also pulls
# every test step's gotestsum JSON artifact and appends a per-test timeline
# chart (on the same absolute-time axis as the pressure chart) to the report.
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

if [ ! -s psimon-pressure.html ]; then
    echo "WARNING: psimon returned empty response" >&2
    rm -f psimon-pressure.html
    exit 0
fi
echo "Saved psimon-pressure.html ($(wc -c <psimon-pressure.html) bytes)" >&2

# Build a combined timeline from every test step's gotestsum JSON. Each test
# step drops its results into /data/buildkite/psimon-results/$BUILD_ID/ via
# stash-test-results.sh — all steps run on the same host, so we just read the
# files off the local disk instead of round-tripping through BK's artifact
# service.
SHARED_DIR="/data/buildkite/psimon-results/${BUILDKITE_BUILD_ID:-}"
if [ -n "${BUILDKITE_BUILD_ID:-}" ] && ls "$SHARED_DIR"/*.json >/dev/null 2>&1; then
    python3 bin/ci-test-timeline \
        test-timeline.frag.html \
        'Go tests — timeline (all steps)' \
        "$SHARED_DIR"/*.json || true
    if [ -s test-timeline.frag.html ]; then
        awk -v frag_path=test-timeline.frag.html '
            BEGIN {
                while ((getline line < frag_path) > 0) frag = frag ORS line
                close(frag_path)
            }
            /<\/body>/ && !done { print frag; done = 1 }
            { print }
        ' psimon-pressure.html >psimon-pressure.html.new
        mv psimon-pressure.html.new psimon-pressure.html
        echo "Appended test timeline ($(wc -c <test-timeline.frag.html) bytes) from $SHARED_DIR" >&2
    fi
    rm -f test-timeline.frag.html
    rm -rf "$SHARED_DIR" 2>/dev/null || true
else
    echo "No test result JSON found in $SHARED_DIR; skipping test timeline" >&2
fi
