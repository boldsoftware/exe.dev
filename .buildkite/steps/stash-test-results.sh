#!/usr/bin/env bash
# stash-test-results.sh — copy a gotestsum JSON file into a shared, build-scoped
# directory on the CI host so the final psimon/test-timeline step can assemble
# a combined visualization without a round-trip through Buildkite's artifact
# storage (all steps share /data/buildkite on the same machine).
#
# Usage: stash-test-results.sh FILE [FILE ...]
# No-op if BUILDKITE_BUILD_ID isn't set or the shared dir can't be created.
set -euo pipefail

: "${BUILDKITE_BUILD_ID:=}"
if [ -z "$BUILDKITE_BUILD_ID" ]; then
    exit 0
fi

DEST="/data/buildkite/psimon-results/${BUILDKITE_BUILD_ID}"
mkdir -p "$DEST" 2>/dev/null || exit 0

for f in "$@"; do
    if [ -s "$f" ]; then
        cp -f "$f" "$DEST/$(basename "$f")"
    fi
done
