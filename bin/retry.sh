#!/bin/bash
#
# retry.sh — Run a command with retries.
#
# Retries the given command up to 5 times with linear backoff
# (1s, 2s, 3s, 4s between attempts).
#
# Usage: retry.sh <command> [args...]
# Examples:
#   retry.sh git fetch origin main
#   retry.sh curl -fsSL https://example.com/install.sh

set -euo pipefail

trap 'exit 130' INT
trap 'exit 143' TERM

if [ $# -eq 0 ]; then
  echo "usage: retry.sh <command> [args...]" >&2
  exit 1
fi

max_attempts=5

for attempt in $(seq 1 "$max_attempts"); do
  if "$@"; then
    exit 0
  fi
  if [ "$attempt" -eq "$max_attempts" ]; then
    echo "retry.sh[$1]: $* failed after $max_attempts attempts" >&2
    exit 1
  fi
  echo "retry.sh[$1]: $* failed (attempt $attempt/$max_attempts), retrying in ${attempt}s..." >&2
  sleep "$attempt"
done
