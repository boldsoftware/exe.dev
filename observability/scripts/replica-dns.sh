#!/bin/bash
# Checks DNS resolution of this host's replica pair and writes
# the result as a Prometheus textfile collector metric.
#
# Only runs on exelet hosts that are NOT in the pdx datacenter
# (pdx hosts use AWS EBS and don't have local replicas).
# The script determines the replica hostname by appending "-replica"
# to the machine's hostname.
set -e

OUTPUT_DIR=/var/lib/prometheus/node-exporter
OUTPUT_FILE=$OUTPUT_DIR/replica_dns.prom

mkdir -p "$OUTPUT_DIR"

HOSTNAME=$(hostname)

# Only run on exelet hosts with replicas (not exe-ctr-* or exelet-pdx-*).
# Also skip replica hosts themselves.
case "$HOSTNAME" in
exe-ctr-* | exelet-pdx-* | *-replica)
    rm -f "$OUTPUT_FILE"
    exit 0
    ;;
exelet-*) ;; # continue
*)
    rm -f "$OUTPUT_FILE"
    exit 0
    ;;
esac

REPLICA="${HOSTNAME}-replica.crocodile-vector.ts.net"

# Try to resolve the replica hostname.
if getent hosts "$REPLICA" >/dev/null 2>&1; then
    RESOLVE_OK=1
else
    RESOLVE_OK=0
fi

cat >"$OUTPUT_FILE.tmp" <<EOF
# HELP exelet_replica_dns_ok Whether the replica pair DNS name resolves (1=ok, 0=fail)
# TYPE exelet_replica_dns_ok gauge
exelet_replica_dns_ok{replica="$REPLICA"} $RESOLVE_OK
EOF

mv "$OUTPUT_FILE.tmp" "$OUTPUT_FILE"
