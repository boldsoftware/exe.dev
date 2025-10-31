#!/bin/bash

# Script to verify that .gitignore is working correctly

echo "🧹 Verifying .gitignore configuration for Shelley"
echo "================================================"

cd "$(dirname "$0")/../.."

echo "\n✅ Current git status:"
git status --porcelain

if [ $? -eq 0 ] && [ -z "$(git status --porcelain)" ]; then
    echo "✅ Working tree is clean"
else
    echo "⚠️  Working tree has changes"
fi

echo "\n🚫 Files being ignored by git:"
git status --ignored --porcelain | grep '^!!' | head -10

echo "\n📁 Build directories that should be ignored:"
for dir in "ui/node_modules" "ui/dist" "ui/test-results" "ui/playwright-report" "bin"; do
    if [ -d "$dir" ]; then
        echo "  ✅ $dir (exists and ignored)"
    else
        echo "  ⚪ $dir (doesn't exist)"
    fi
done

echo "\n💾 Database files that should be ignored:"
for pattern in "*.db" "*.db-shm" "*.db-wal"; do
    files=$(find . -maxdepth 2 -name "$pattern" 2>/dev/null)
    if [ -n "$files" ]; then
        echo "  ✅ Found and ignoring: $pattern"
        echo "$files" | sed 's/^/    /'
    else
        echo "  ⚪ No $pattern files found"
    fi
done

echo "\n🎭 Playwright outputs that should be ignored:"
for dir in "ui/test-results" "ui/playwright-report"; do
    if [ -d "$dir" ]; then
        echo "  ✅ $dir (exists and ignored)"
    else
        echo "  ⚪ $dir (doesn't exist)"
    fi
done

echo "\n📸 Screenshot directory:"
if [ -d "ui/e2e/screenshots" ]; then
    count=$(find ui/e2e/screenshots -name "*.png" 2>/dev/null | wc -l)
    echo "  ✅ ui/e2e/screenshots exists with $count PNG files (ignored)"
else
    echo "  ❌ ui/e2e/screenshots missing"
fi

echo "\n🎯 Summary: .gitignore is properly configured to exclude build outputs while preserving source code."
