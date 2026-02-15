#!/bin/bash
#
# retry.sh — Run a command with retries.
#
# Retries the given command up to 5 times with linear backoff
# (1s, 2s, 3s, 4s between attempts).
#
# By default, any non-zero exit code triggers a retry. Use --retry-on
# to restrict retries to specific exit codes (e.g. transport errors).
#
# Usage: retry.sh [--retry-on <code>] <command> [args...]
# Examples:
#   retry.sh git fetch origin main
#   retry.sh curl -fsSL https://example.com/install.sh
#   retry.sh --retry-on 128 git push origin HEAD:main

set -euo pipefail

trap 'exit 130' INT
trap 'exit 143' TERM

retry_on=""
if [ "${1:-}" = "--retry-on" ]; then
  if [ $# -lt 2 ]; then
    echo "retry.sh: --retry-on requires an argument" >&2
    exit 1
  fi
  retry_on="$2"
  case "$retry_on" in
    ''|*[!0-9]*) echo "retry.sh: --retry-on requires a numeric exit code, got '$retry_on'" >&2; exit 1 ;;
  esac
  shift 2
fi

if [ $# -eq 0 ]; then
  echo "usage: retry.sh [--retry-on <code>] <command> [args...]" >&2
  exit 1
fi

max_attempts=5

for attempt in $(seq 1 "$max_attempts"); do
  rc=0
  "$@" || rc=$?
  if [ "$rc" -eq 0 ]; then
    exit 0
  fi
  if [ -n "$retry_on" ] && [ "$rc" -ne "$retry_on" ]; then
    exit "$rc"
  fi
  if [ "$attempt" -eq "$max_attempts" ]; then
    echo "retry.sh[$1]: $* failed after $max_attempts attempts (exit code $rc)" >&2
    exit "$rc"
  fi
  echo "retry.sh[$1]: $* failed (attempt $attempt/$max_attempts, exit code $rc), retrying in ${attempt}s..." >&2
  sleep "$attempt"
done
