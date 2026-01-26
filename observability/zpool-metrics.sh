#!/bin/bash
# Writes zpool metrics to textfile collector format
OUTPUT_DIR=/var/lib/prometheus/node-exporter
OUTPUT_FILE=$OUTPUT_DIR/zpool.prom

mkdir -p "$OUTPUT_DIR"

# Get zpool capacity (0-100 integer)
CAPACITY=$(zpool list -H -o capacity tank 2>/dev/null | tr -d '%')

if [ -n "$CAPACITY" ]; then
    cat > "$OUTPUT_FILE.tmp" <<EOF
# HELP zpool_capacity_percent ZFS pool capacity percentage used
# TYPE zpool_capacity_percent gauge
zpool_capacity_percent{pool="tank"} $CAPACITY
EOF
    mv "$OUTPUT_FILE.tmp" "$OUTPUT_FILE"
fi
