#!/usr/bin/env bash
#
# Clean up stale VMs and disk images on edric.
# Run via cron to prevent resource leaks.
#
set -euo pipefail

LOG="/var/log/edric-ci-cleanup.log"
exec >>"$LOG" 2>&1
echo "=== $(date) === cleanup starting ==="

# Destroy any cloud-hypervisor VMs that have been running for more than 30 minutes.
# Parse the creation timestamp from the pidfile name.
NOW=$(date +%s)
THRESHOLD=1800

for PIDFILE in /tmp/ch-pid-*; do
    [[ -f "$PIDFILE" ]] || continue
    VM=$(basename "$PIDFILE" | sed 's/^ch-pid-//')

    # Extract 14-digit timestamp (YYYYMMDDHHMMSS) from end of VM name.
    TS=$(echo "$VM" | grep -oP '\d{14}$' || true)
    if [[ -z "$TS" ]]; then
        continue
    fi

    VM_EPOCH=$(date -d "${TS:0:4}-${TS:4:2}-${TS:6:2} ${TS:8:2}:${TS:10:2}:${TS:12:2}" +%s 2>/dev/null || echo "0")
    if [[ "$VM_EPOCH" -eq 0 ]]; then
        continue
    fi

    AGE=$((NOW - VM_EPOCH))
    if [[ $AGE -gt $THRESHOLD ]]; then
        PID=$(cat "$PIDFILE" 2>/dev/null || true)
        echo "Destroying stale VM: $VM (age: ${AGE}s, PID: $PID)"
        if [[ -n "$PID" ]] && [[ -d "/proc/$PID" ]]; then
            kill -9 "$PID" 2>/dev/null || true
        fi
        rm -f "$PIDFILE" "/tmp/ch-${VM}.log" "/tmp/ch-api-${VM}.sock"
    fi
done

# Clean up orphaned disk images (no matching CH process running).
for IMG in /var/lib/libvirt/images/e1e-runner*.qcow2 /var/lib/libvirt/images/ci-ubuntu-runner*.qcow2; do
    [[ -f "$IMG" ]] || continue
    VM=$(basename "$IMG" .qcow2)
    # Strip -data suffix to get the base VM name
    VM=$(echo "$VM" | sed 's/-data$//')
    PIDFILE="/tmp/ch-pid-${VM}"
    if [[ ! -f "$PIDFILE" ]]; then
        AGE=$((NOW - $(stat -c %Y "$IMG")))
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
    PIDFILE="/tmp/ch-pid-${VM}"
    if [[ ! -f "$PIDFILE" ]]; then
        echo "Removing orphaned seed ISO: $ISO"
        rm -f "$ISO"
    fi
done

# Clean up old snapshot caches: keep only the 2 most recent per runner.
# Using count-based cleanup instead of mtime-based, because on busy days
# multiple ops/ tree hash and exeuntu digest changes create many snapshot
# dirs (~5.5GB each) all with today's mtime, so mtime-based cleanup
# doesn't kick in fast enough.
for USER_HOME in /home/runner*; do
    CACHE="${USER_HOME}/.cache/ci-snapshots"
    [[ -d "$CACHE" ]] || continue
    # List ci-vm-* dirs newest first, skip the 2 newest, remove the rest.
    find "$CACHE" -maxdepth 1 -type d -name 'ci-vm-*' -printf '%T@ %p\n' 2>/dev/null |
        sort -rn | tail -n +3 | cut -d' ' -f2- | while read -r dir; do
        echo "Removing old snapshot cache: $dir"
        rm -rf "$dir"
    done
done

echo "=== $(date) === cleanup complete ==="
