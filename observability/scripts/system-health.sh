#!/bin/bash
# Writes system health metrics to textfile collector format
OUTPUT_DIR=/var/lib/prometheus/node-exporter
OUTPUT_FILE=$OUTPUT_DIR/system_health.prom

mkdir -p "$OUTPUT_DIR"

cat >"$OUTPUT_FILE.tmp" <<EOF
# HELP system_failed_units Number of failed systemd units
# TYPE system_failed_units gauge
# HELP system_stuck_units Number of systemd units stuck in activating/deactivating state
# TYPE system_stuck_units gauge
# HELP system_oom_kills_total OOM kills detected in journal since last boot
# TYPE system_oom_kills_total gauge
# HELP system_disk_io_errors_total Disk I/O errors detected in journal since last boot
# TYPE system_disk_io_errors_total gauge
# HELP system_memory_errors_total MCE/ECC memory errors detected in journal since last boot
# TYPE system_memory_errors_total gauge
# HELP system_zombie_processes Number of zombie processes
# TYPE system_zombie_processes gauge
EOF

# Failed systemd units
FAILED=$(systemctl --state=failed --no-legend 2>/dev/null | wc -l)

# Stuck units (activating/deactivating for too long)
STUCK=$(systemctl --state=activating,deactivating --no-legend 2>/dev/null | wc -l)

# OOM kills since last boot
OOM=$(journalctl -b -k --no-pager -q 2>/dev/null | grep -c 'Out of memory\|oom-kill\|invoked oom-killer' || true)

# Disk I/O errors since last boot
DISK_ERRORS=$(journalctl -b -k --no-pager -q 2>/dev/null | grep -c 'I/O error\|medium error\|hardware error\|DRDY ERR\|UNC ERR' || true)

# MCE/ECC memory errors since last boot
MEM_ERRORS=$(journalctl -b -k --no-pager -q 2>/dev/null | grep -c 'mce:.*Hardware Error\|EDAC.*error\|CE error\|UE error' || true)

# Zombie processes
ZOMBIES=$(ps aux 2>/dev/null | awk '$8 ~ /^Z/ {count++} END {print count+0}')

cat >>"$OUTPUT_FILE.tmp" <<EOF
system_failed_units $FAILED
system_stuck_units $STUCK
system_oom_kills_total $OOM
system_disk_io_errors_total $DISK_ERRORS
system_memory_errors_total $MEM_ERRORS
system_zombie_processes $ZOMBIES
EOF

mv "$OUTPUT_FILE.tmp" "$OUTPUT_FILE"
