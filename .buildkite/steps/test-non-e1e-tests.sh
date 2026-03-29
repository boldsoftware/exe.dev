#!/usr/bin/env bash
set -euo pipefail

# Runs unit tests (excluding e1e) with optional sharding.
# UNIT_TEST_SHARD=A → execore Test[A-H] (~185 tests)
# UNIT_TEST_SHARD=B → all other packages (~18s)
# UNIT_TEST_SHARD=C → execore Test[I-P] (~98 tests)
# UNIT_TEST_SHARD=D → execore Test[Q-Z] (~104 tests)
# Unset / empty → run all (local dev default)

export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"

UNIT_TEST_SHARD="${UNIT_TEST_SHARD:-}"

echo "--- :go: Set up Go"
go version

# Only shard B (other pkgs) needs exelet-fs and exe-init because some packages
# (exelet/fs, exelet/services/compute, cmd/exelet) embed those files.
# Execore shards (A, C, D) don't import exelet/fs, so skip the download (~5s savings).
if [ "$UNIT_TEST_SHARD" = "B" ] || [ -z "$UNIT_TEST_SHARD" ]; then
    echo "--- :package: Ensure b2 CLI available"
    if ! command -v b2 >/dev/null 2>&1; then
        ./bin/retry.sh bash -c 'set -o pipefail; curl -LsSf https://astral.sh/uv/install.sh | sh'
        export PATH="$HOME/.local/bin:$PATH"
        ./bin/retry.sh uv tool install b2
    fi

    echo "--- :floppy_disk: Download exelet-fs"
    make exelet-fs

    echo "--- :hammer: Build exe-init"
    make exe-init
fi

echo "--- :go: Run unit tests (excluding e1e)${UNIT_TEST_SHARD:+ shard $UNIT_TEST_SHARD}"

ALL_PKGS=$(go list ./... | grep -v -E '^exe\.dev/(e1e|e1e/testinfra|e1e/exelets|experiments/imageunpack)$')

RUN_FILTER=""
case "$UNIT_TEST_SHARD" in
A)
    PKGS="exe.dev/execore"
    RUN_FILTER="-run ^Test[A-Ha-h]"
    ;;
B) PKGS=$(echo "$ALL_PKGS" | grep -v '^exe\.dev/execore$') ;;
C)
    PKGS="exe.dev/execore"
    RUN_FILTER="-run ^Test[I-Pi-p]"
    ;;
D)
    PKGS="exe.dev/execore"
    RUN_FILTER="-run ^Test[Q-Zq-z]"
    ;;
"") PKGS="$ALL_PKGS" ;;
*)
    echo "Unknown UNIT_TEST_SHARD=$UNIT_TEST_SHARD (expected A-D or unset)"
    exit 1
    ;;
esac

# shellcheck disable=SC2086
JSON_OUT="unit-results-${UNIT_TEST_SHARD:-all}.json"
XML_OUT="unit-results-${UNIT_TEST_SHARD:-all}.xml"

COVER_FLAGS=""
if [ "${E1E_COVERAGE:-}" = "true" ]; then
    COVER_PROFILE="coverage-unit-${UNIT_TEST_SHARD:-all}.txt"
    COVER_FLAGS="-coverprofile=$COVER_PROFILE"
    echo "Coverage mode: writing profile to $COVER_PROFILE"
fi

go tool gotestsum --format testname --jsonfile "$JSON_OUT" --junitfile "$XML_OUT" -- -race -count=1 ${COVER_FLAGS:+$COVER_FLAGS} ${RUN_FILTER:+$RUN_FILTER} $PKGS
TEST_EXIT=$?
python3 bin/ci-test-gantt "$JSON_OUT" "test-gantt-unit-${UNIT_TEST_SHARD:-all}.html" "unit tests (shard ${UNIT_TEST_SHARD:-all})" 2>/dev/null || true
.buildkite/steps/upload-test-analytics.sh "$XML_OUT" || true
exit $TEST_EXIT
