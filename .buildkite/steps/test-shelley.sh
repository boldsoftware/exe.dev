#!/usr/bin/env bash
set -euo pipefail
trap 'echo "Error at line $LINENO"' ERR

# Shelley Go tests + UI checks (mirrors GHA test-go job).

echo "--- :gear: Setup dependencies"
source .buildkite/steps/setup-shelley-deps.sh

cd shelley

echo "--- :art: Install UI dependencies"
cd ui
pnpm install --frozen-lockfile

echo "--- :typescript: TypeScript type check"
pnpm run type-check

echo "--- :eslint: ESLint"
pnpm run lint

echo "--- :test_tube: UI unit tests"
pnpm test

echo "--- :package: Build UI"
pnpm run build
cd ..

echo "--- :hammer: Build template tarballs"
make templates

echo "--- :gear: Verify go generate unchanged"
go generate ./...
if [ -n "$(git status --porcelain)" ]; then
  echo "ERROR: go generate produced uncommitted changes" >&2
  git status --porcelain >&2
  git diff >&2
  exit 1
fi

echo "--- :white_check_mark: go vet"
go vet ./...

echo "--- :test_tube: Go tests"
go tool gotestsum --format testname -- -race ./...
