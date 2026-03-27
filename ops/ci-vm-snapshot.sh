#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ "${VM_DRIVER:-}" == "cloudhypervisor" ]]; then
    # ci-vm.py creates the snapshot on first boot; no warmup needed.
    exit 0
fi

source ${SCRIPT_DIR}/ci-vm-env.sh

if [[ ${SNAPSHOT_AVAILABLE} -eq 1 ]]; then
    # Snapshot is available, nothing to do.
    exit 0
fi

# Start a new VM, and then destroy it.

OUTDIR="${OUTDIR:-$PWD}"
export NAME OUTDIR
${SCRIPT_DIR}/ci-vm-start.sh
${SCRIPT_DIR}/ci-vm-destroy.sh ${OUTDIR}/${NAME}.env
