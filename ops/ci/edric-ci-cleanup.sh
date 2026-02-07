#!/usr/bin/env bash
#
# Clean up stale VMs and disk images on edric.
# Run via cron to prevent resource leaks.
#
set -euo pipefail

LOG="/var/log/edric-ci-cleanup.log"
exec >> "$LOG" 2>&1
echo "=== $(date) === cleanup starting ==="

# Destroy any VMs that have been running for more than 30 minutes.
# Parse the creation timestamp from the VM name instead of using disk mtime,
# because running VMs continuously write to their disks keeping mtime fresh.
NOW=$(date +%s)
THRESHOLD=1800

for VM in $(virsh list --name 2>/dev/null | grep -v "^$"); do
    # Extract 14-digit timestamp (YYYYMMDDHHMMSS) from end of VM name.
    # Matches both ci-ubuntu-runnerN-YYYYMMDDHHMMSS and e1e-runnerN-XXXX-YYYYMMDDHHMMSS.
    TS=$(echo "$VM" | grep -oP '\d{14}$' || true)
    if [[ -z "$TS" ]]; then
        continue
    fi

    VM_EPOCH=$(date -d "${TS:0:4}-${TS:4:2}-${TS:6:2} ${TS:8:2}:${TS:10:2}:${TS:12:2}" +%s 2>/dev/null || echo "0")
    if [[ "$VM_EPOCH" -eq 0 ]]; then
        continue
    fi

    AGE=$(( NOW - VM_EPOCH ))
    if [[ $AGE -gt $THRESHOLD ]]; then
        echo "Destroying stale VM: $VM (age: ${AGE}s)"
        virsh destroy "$VM" || true
    fi
done

# Clean up orphaned disk images (no matching VM running).
# Match both e1e-runner* and ci-ubuntu-* images.
for IMG in /var/lib/libvirt/images/e1e-runner*.qcow2 /var/lib/libvirt/images/ci-ubuntu-runner*.qcow2; do
    [[ -f "$IMG" ]] || continue
    VM=$(basename "$IMG" .qcow2)
    # Strip -data suffix to get the base VM name
    VM=$(echo "$VM" | sed 's/-data$//')
    if ! virsh list --name 2>/dev/null | grep -q "^${VM}$"; then
        AGE=$(( NOW - $(stat -c %Y "$IMG") ))
        if [[ $AGE -gt $THRESHOLD ]]; then
            echo "Removing orphaned image: $IMG (age: ${AGE}s)"
            rm -f "$IMG"
        fi
    fi
done

# Clean up orphaned seed ISOs
for ISO in /var/lib/libvirt/images/e1e-runner*-seed.iso /var/lib/libvirt/images/ci-ubuntu-runner*-seed.iso; do
    [[ -f "$ISO" ]] || continue
    VM=$(basename "$ISO" -seed.iso)
    if ! virsh list --name 2>/dev/null | grep -q "^${VM}$"; then
        echo "Removing orphaned seed ISO: $ISO"
        rm -f "$ISO"
    fi
done

# Clean up old snapshot caches (keep only today's and yesterday's)
for USER_HOME in /home/runner*; do
    find "${USER_HOME}/.cache/exedev/" -maxdepth 1 -name "ci-vm-*" -mtime +2 -exec rm -rf {} \; 2>/dev/null || true
done

echo "=== $(date) === cleanup complete ==="
