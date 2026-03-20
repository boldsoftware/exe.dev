#!/bin/bash
# Writes OS update metrics to textfile collector format
OUTPUT_DIR=/var/lib/prometheus/node-exporter
OUTPUT_FILE=$OUTPUT_DIR/os_updates.prom

mkdir -p "$OUTPUT_DIR"

cat >"$OUTPUT_FILE.tmp" <<EOF
# HELP os_updates_pending Number of pending OS package updates
# TYPE os_updates_pending gauge
# HELP os_security_updates_pending Number of pending security updates
# TYPE os_security_updates_pending gauge
# HELP os_reboot_required Whether a reboot is required (0=no, 1=yes)
# TYPE os_reboot_required gauge
EOF

# Refresh package lists (quiet, don't fail the whole script)
apt-get update -qq 2>/dev/null

# Count total pending updates
TOTAL=$(/usr/bin/apt list --upgradable 2>/dev/null | grep -c 'upgradable' || echo 0)

# Count security updates
SECURITY=$(/usr/bin/apt list --upgradable 2>/dev/null | grep -c '\-security' || echo 0)

# Check reboot required
if [ -f /var/run/reboot-required ]; then
    REBOOT=1
else
    REBOOT=0
fi

cat >>"$OUTPUT_FILE.tmp" <<EOF
os_updates_pending $TOTAL
os_security_updates_pending $SECURITY
os_reboot_required $REBOOT
EOF

mv "$OUTPUT_FILE.tmp" "$OUTPUT_FILE"
