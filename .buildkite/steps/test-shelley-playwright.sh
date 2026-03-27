#!/usr/bin/env bash
set -euo pipefail
trap 'echo "Error at line $LINENO"' ERR

# Shelley Playwright E2E tests (mirrors GHA test-ui job).

echo "--- :gear: Setup dependencies"
source .buildkite/steps/setup-shelley-deps.sh

cd shelley

echo "--- :art: Install UI dependencies and build"
cd ui
pnpm install --frozen-lockfile
pnpm run build
cd ..

echo "--- :hammer: Build template tarballs"
make templates

echo "--- :wrench: Build shelley binary"
go build -o bin/shelley ./cmd/shelley

echo "--- :chrome: Install Playwright browsers"
cd ui
npx playwright install --with-deps chromium

echo "--- :performing_arts: Run Playwright E2E tests"
npx playwright test
