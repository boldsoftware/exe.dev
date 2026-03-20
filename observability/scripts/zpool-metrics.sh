#!/bin/bash
# Writes zpool metrics to textfile collector format
OUTPUT_DIR=/var/lib/prometheus/node-exporter
OUTPUT_FILE=$OUTPUT_DIR/zpool.prom

mkdir -p "$OUTPUT_DIR"

cat >"$OUTPUT_FILE.tmp" <<EOF
# HELP zpool_capacity_percent ZFS pool capacity percentage used
# TYPE zpool_capacity_percent gauge
# HELP zpool_health_degraded Whether the ZFS pool health is not ONLINE (0=healthy, 1=degraded/faulted)
# TYPE zpool_health_degraded gauge
# HELP zpool_fragmentation_percent ZFS pool fragmentation percentage
# TYPE zpool_fragmentation_percent gauge
# HELP zpool_state ZFS pool state (1 = this is the current state)
# TYPE zpool_state gauge
EOF

# All states the dashboard may query; lowercase to match zpool output after tolower.
ALL_STATES="online degraded faulted suspended unavail removed offline"

for POOL in tank backup; do
    CAPACITY=$(zpool list -H -o capacity "$POOL" 2>/dev/null | tr -d '%')
    [ -z "$CAPACITY" ] && continue

    HEALTH=$(zpool list -H -o health "$POOL" 2>/dev/null)
    HEALTH_LOWER=$(echo "$HEALTH" | tr '[:upper:]' '[:lower:]')
    if [ "$HEALTH" = "ONLINE" ]; then
        HEALTH_DEGRADED=0
    else
        HEALTH_DEGRADED=1
    fi

    FRAG=$(zpool list -H -o frag "$POOL" 2>/dev/null | tr -d '%')

    cat >>"$OUTPUT_FILE.tmp" <<EOF
zpool_capacity_percent{pool="$POOL"} $CAPACITY
zpool_health_degraded{pool="$POOL",health="$HEALTH"} $HEALTH_DEGRADED
zpool_fragmentation_percent{pool="$POOL"} ${FRAG:-0}
EOF

    # Emit one zpool_state line per possible state so the dashboard can
    # count pools by state, matching the node_zfs_zpool_state metric shape.
    for STATE in $ALL_STATES; do
        if [ "$STATE" = "$HEALTH_LOWER" ]; then
            VALUE=1
        else
            VALUE=0
        fi
        echo "zpool_state{pool=\"$POOL\",state=\"$STATE\"} $VALUE" >>"$OUTPUT_FILE.tmp"
    done
done

# Only write the file if we got at least one pool
if grep -q 'zpool_capacity_percent{' "$OUTPUT_FILE.tmp"; then
    mv "$OUTPUT_FILE.tmp" "$OUTPUT_FILE"
else
    rm -f "$OUTPUT_FILE.tmp"
fi
