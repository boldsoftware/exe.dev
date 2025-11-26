#!/usr/bin/env bash
#
# run-coverage.sh - Run Go tests individually with coverage output
#
# Usage:
#   ./scripts/run-coverage.sh [output_dir]
#
# This script:
# 1. Lists all test functions in each package
# 2. Runs each test individually with coverage
# 3. Outputs coverage files in Go's text format to output_dir
#
# For e1e tests: uses the built-in -coverage-out flag which collects
# coverage from both exed and exelet via GOCOVERDIR
#
set -euo pipefail

OUTPUT_DIR="${1:-coverage}"
mkdir -p "$OUTPUT_DIR"

# Get project root (where go.mod is)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

echo "=== Coverage Test Runner ==="
echo "Output directory: $OUTPUT_DIR"
echo ""

# Get all packages except e1e (which needs special handling)
PKGS=$(go list ./... | grep -v -E '^exe\.dev/e1e$')
E1E_PKG="exe.dev/e1e"

run_test_with_coverage() {
    local pkg="$1"
    local test_name="$2"
    local output_file="$3"

    echo "  Running: $test_name"

    if ! go test -count=1 -race -cover -coverprofile="$output_file" -covermode=atomic -run "^${test_name}\$" "$pkg" > /dev/null 2>&1; then
        echo "    FAILED (coverage may still be written)"
        return 1
    fi
    return 0
}

run_pkg_tests() {
    local pkg="$1"
    local pkg_short="${pkg#exe.dev/}"
    pkg_short="${pkg_short//\//_}"

    # List tests in this package
    local tests
    tests=$(go test -list '.*' "$pkg" 2>/dev/null | grep -E '^Test' || true)

    if [ -z "$tests" ]; then
        echo "  (no tests)"
        return 0
    fi

    local failed=0
    while IFS= read -r test_name; do
        [ -z "$test_name" ] && continue
        local output_file="$OUTPUT_DIR/${pkg_short}_${test_name}.cover"
        if ! run_test_with_coverage "$pkg" "$test_name" "$output_file"; then
            failed=1
        fi
    done <<< "$tests"

    return $failed
}

echo "=== Unit test packages ==="
failed_any=0
for pkg in $PKGS; do
    echo "Package: $pkg"
    if ! run_pkg_tests "$pkg"; then
        failed_any=1
    fi
done

echo ""
echo "=== e1e tests ==="
echo "Package: $E1E_PKG"

# List and run e1e tests - these use -coverage-out flag
e1e_tests=$(go test -list '.*' "$E1E_PKG" 2>/dev/null | grep -E '^Test' || true)
if [ -n "$e1e_tests" ]; then
    mkdir -p "$OUTPUT_DIR/e1e"
    while IFS= read -r test_name; do
        [ -z "$test_name" ] && continue
        cover_out="$OUTPUT_DIR/e1e/e1e_${test_name}.cover"
        echo "  Running: $test_name"
        # e1e tests use -coverage-out flag for merged exed+exelet coverage
        if ! go test -count=1 -race -run "^${test_name}\$" ./e1e -coverage-out="$cover_out" 2>&1 | tail -1; then
            echo "    FAILED"
            failed_any=1
        fi
    done <<< "$e1e_tests"
else
    echo "  (no tests found or e1e tests skipped)"
fi

echo ""
echo "=== Generating HTML report ==="
cover_files=$(find "$OUTPUT_DIR" -name "*.cover" -type f 2>/dev/null)
cover_count=$(echo "$cover_files" | grep -c . || echo 0)
echo "Coverage files written: $cover_count"

if [ "$cover_count" -gt 0 ]; then
    # shellcheck disable=SC2086
    go run ./cmd/undercover -o "$OUTPUT_DIR/coverage.html" $cover_files
    echo "HTML report: $OUTPUT_DIR/coverage.html"
fi

echo ""
echo "=== Summary ==="
echo "Output directory: $OUTPUT_DIR"

if [ "$failed_any" -eq 1 ]; then
    echo "WARNING: Some tests failed"
    exit 1
fi
