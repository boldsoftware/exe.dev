#!/usr/bin/env bash
set -euo pipefail
trap 'echo Error in $0 at line $LINENO' ERR

# Dedicated UI test step. Runs typecheck and vitest unit tests for ui/.
# Kicked off only when files under ui/ change (see generate-pipeline.py).
#
# Note: the production UI build itself runs inside build-e1e-binaries
# (because exed embeds ui/dist via //go:embed). This step is purely for
# the UI's own checks.

echo "--- :vue: Typecheck ui/"
make -C ui typecheck

echo "--- :vue: Unit tests ui/"
make -C ui test
