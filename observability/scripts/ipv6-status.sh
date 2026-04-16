#!/bin/bash
# Writes IPv6-enabled status to textfile collector format.
# Reports 1 if any non-loopback, non-virtual interface has a global-scope
# IPv6 address. Virtual/overlay interfaces (tap*, veth*, br-*, docker*, cni*,
# tailscale0, zt*, vnet*) are excluded because we only care about the host's
# real connectivity, not VM/container or mesh addresses.
OUTPUT_DIR=/var/lib/prometheus/node-exporter
OUTPUT_FILE=$OUTPUT_DIR/ipv6_status.prom

mkdir -p "$OUTPUT_DIR"

cat >"$OUTPUT_FILE.tmp" <<EOF
# HELP host_ipv6_enabled 1 if any non-loopback physical interface has a global-scope IPv6 address
# TYPE host_ipv6_enabled gauge
# HELP host_ipv6_global_addresses Number of global-scope IPv6 addresses on non-loopback physical interfaces
# TYPE host_ipv6_global_addresses gauge
EOF

COUNT=0
if command -v ip >/dev/null 2>&1; then
    COUNT=$(ip -6 -br addr show scope global 2>/dev/null |
        awk '{print $1}' |
        grep -vE '^(lo|tap|veth|ifb|br-|docker|cni|tailscale0|zt|vnet)' |
        wc -l)
fi

if [ "$COUNT" -gt 0 ]; then
    ENABLED=1
else
    ENABLED=0
fi

cat >>"$OUTPUT_FILE.tmp" <<EOF
host_ipv6_enabled $ENABLED
host_ipv6_global_addresses $COUNT
EOF

mv "$OUTPUT_FILE.tmp" "$OUTPUT_FILE"
