#!/usr/bin/env bash
set -euo pipefail

# Runs all code-quality checks for the non-e1e codebase:
# go generate, make protos, cmd builds, oss tests, and linters.
# All checks that don't depend on each other run in parallel.

export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"

echo "--- :go: Set up Go"
go version

echo "--- :package: Ensure b2 CLI available"
if ! command -v b2 >/dev/null 2>&1; then
    ./bin/retry.sh bash -c 'set -o pipefail; curl -LsSf https://astral.sh/uv/install.sh | sh'
    export PATH="$HOME/.local/bin:$PATH"
    ./bin/retry.sh uv tool install b2
fi
b2 version

echo "--- :floppy_disk: Download exelet-fs"
make exelet-fs

echo "--- :hammer: Build exe-init"
make exe-init

echo "--- :white_check_mark: Run checks in parallel"
# go generate, make protos, cmd builds, oss tests, and static checks are all
# independent — they target different files / modules and can run concurrently.
pids=()
names=()

go generate ./... &
pids+=($!)
names+=("go generate")

make protos &
pids+=($!)
names+=("make protos")

(for dir in cmd/*/; do go build -o /dev/null "./$dir"; done) &
pids+=($!)
names+=("cmd builds")

(for dir in oss/*/; do
    if [ -f "$dir/go.mod" ]; then
        echo "=== testing $dir ==="
        (cd "$dir" && go test ./...)
    fi
done) &
pids+=($!)
names+=("oss tests")

# UI typecheck and build (exe dashboard)
# make ui runs: pnpm install, vue-tsc --noEmit, vite build
if [ -f ui/package.json ]; then
    make ui &
    pids+=($!)
    names+=("ui typecheck+build")
fi

fail=0
for i in "${!pids[@]}"; do
    if wait "${pids[$i]}"; then
        echo "  OK: ${names[$i]}"
    else
        echo "  FAILED: ${names[$i]}" >&2
        fail=1
    fi
done
[ $fail -eq 0 ] || exit 1

echo "--- :white_check_mark: Verify generated code unchanged"
if [ -n "$(git status --porcelain)" ]; then
    echo "ERROR: 'go generate ./...' or 'make protos' produced uncommitted changes. Commit generated code." >&2
    git status --porcelain >&2
    git diff >&2
    exit 1
fi

echo "--- :mag: Verify non-test code doesn't import tslog"
if git grep -n -- 'exe.dev/tslog' -- '*.go' ':!*_test.go'; then
    echo "ERROR: production code should not import exe.dev/tslog" >&2
    exit 1
fi

echo "--- :mag: Verify package stage has no non-stdlib deps"
if go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./stage/ | grep -v '^exe.dev/stage$' | grep .; then
    exit 1
fi

echo "--- :lint-roller: Verify linters pass (parallel)"
pids=()
names=()

go vet ./... &
pids+=($!)
names+=("go vet")

(EXELINT=$(mktemp /tmp/exelint.XXXXXX) && go build -o "$EXELINT" ./cmd/exelint && go vet -vettool="$EXELINT" ./... && rm -f "$EXELINT") &
pids+=($!)
names+=("exelint")

fail=0
for i in "${!pids[@]}"; do
    if wait "${pids[$i]}"; then
        echo "  OK: ${names[$i]}"
    else
        echo "  FAILED: ${names[$i]}" >&2
        fail=1
    fi
done
[ $fail -eq 0 ] || exit 1
