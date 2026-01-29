#!/bin/bash
# Run cohort analysis on exe-logs2 VM
#
# Usage: ./run-cohort-analysis.sh [--weeks|--days|--summary]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ssh support+exe-logs2@exe.xyz python3 - "$@" <"$SCRIPT_DIR/cohort_analysis.py"
