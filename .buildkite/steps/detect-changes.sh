#!/usr/bin/env bash
set -euo pipefail

# Detect which files changed vs origin/main and set Buildkite metadata.
# Downstream steps read these to decide whether to run.

git fetch origin main

CHANGED_FILES=$(git diff --name-only origin/main...HEAD)

SHELLEY_CHANGED=false
EXE_CHANGED=false

if [ -z "$CHANGED_FILES" ]; then
  echo "No files changed vs origin/main, defaulting to exe tests"
  EXE_CHANGED=true
else
  while IFS= read -r file; do
    case "$file" in
      shelley/*|.github/workflows/shelley-tests.yml)
        SHELLEY_CHANGED=true
        ;;
      *)
        EXE_CHANGED=true
        ;;
    esac
  done <<< "$CHANGED_FILES"
fi

echo "shelley_changed=$SHELLEY_CHANGED"
echo "exe_changed=$EXE_CHANGED"

buildkite-agent meta-data set shelley_changed "$SHELLEY_CHANGED"
buildkite-agent meta-data set exe_changed "$EXE_CHANGED"
