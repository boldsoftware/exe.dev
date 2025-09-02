#!/usr/bin/env bash
set -euo pipefail
ENVFILE="${1:?usage: vm_destroy.sh /path/to/envfile}"
# shellcheck disable=SC1090
source "${ENVFILE}"

# Stop & undefine
sudo virsh destroy "${VM_NAME}" >/dev/null 2>&1 || true
sudo virsh undefine "${VM_NAME}" --nvram >/dev/null 2>&1 || true

# Remove disks
sudo rm -f "${VM_DISK}" "${VM_SEED}" || true
rm -f "${ENVFILE}" || true
