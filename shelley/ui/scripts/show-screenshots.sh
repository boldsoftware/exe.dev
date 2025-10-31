#!/bin/bash

# Script to help inspect Playwright test screenshots

echo "📸 Shelley E2E Test Screenshots"
echo "================================="

cd "$(dirname "$0")/.."

# Create screenshots directory if it doesn't exist
mkdir -p e2e/screenshots

# Check for test results
if [ -d "test-results" ]; then
    echo "\n🔍 Recent test failures:"
    find test-results -name "*.png" -type f -exec ls -la {} \; | head -10
else
    echo "\n❌ No test-results directory found. Run tests first:"
    echo "   npm run test:e2e"
fi

# Check for screenshots in e2e directory
if [ "$(ls e2e/screenshots/*.png 2>/dev/null | wc -l)" -gt 0 ]; then
    echo "\n📷 Generated screenshots:"
    ls -la e2e/screenshots/*.png | head -10
else
    echo "\n📷 No screenshots found in e2e/screenshots/"
fi

echo "\n💡 To view screenshots:"
echo "   - Open files directly with an image viewer"
echo "   - Use 'npx playwright show-report' for HTML report"
echo "   - Check test-results/ for failure screenshots"
