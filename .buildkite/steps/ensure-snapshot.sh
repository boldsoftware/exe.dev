#!/usr/bin/env bash
set -euo pipefail

# Ensure the CI VM snapshot exists, provisioning one if needed.
# Runs in parallel with build-e1e-binaries so snapshot creation
# overlaps with Go compilation.

export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"

echo "--- :package: Ensure b2 CLI available"
if ! command -v b2 >/dev/null 2>&1; then
    ./bin/retry.sh bash -c 'set -o pipefail; curl -LsSf https://astral.sh/uv/install.sh | sh'
    export PATH="$HOME/.local/bin:$PATH"
    ./bin/retry.sh uv tool install b2
fi

echo "--- :floppy_disk: Download exelet-fs (for product kernel)"
make exelet-fs

echo "--- :camera: Ensure VM snapshot"
python3 ops/ci-vm.py ensure-snapshot
