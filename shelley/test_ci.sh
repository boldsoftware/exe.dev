#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

echo "==> Running Shelley CI Tests"
echo ""

echo "==> Installing UI dependencies..."
cd ui
npm ci
cd ..

echo ""
echo "==> Running TypeScript type check..."
cd ui
npm run type-check
cd ..

echo ""
echo "==> Running ESLint..."
cd ui
npm run lint
cd ..

echo ""
echo "==> Building UI..."
cd ui
npm run build
cd ..

echo ""
echo "==> Running Go tests..."
go test -v ./...

echo ""
echo "==> Running Playwright E2E tests..."
cd ui
npx playwright install --with-deps chromium
npx playwright test
cd ..

echo ""
echo "==> All Shelley tests passed! ✓"
