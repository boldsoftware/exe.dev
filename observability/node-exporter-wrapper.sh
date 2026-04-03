#!/bin/bash
# Wrapper for prometheus-node-exporter that binds to the Tailscale IP.
# Deployed by deploy-node-exporter.py — do not edit on hosts directly.
#
# The PORT variable is set by the systemd override's Environment= directive.
set -euo pipefail

TAILSCALE_IP=$(tailscale ip -4)
if [ -z "$TAILSCALE_IP" ]; then
    echo "ERROR: Failed to get Tailscale IP" >&2
    exit 1
fi

exec /usr/bin/prometheus-node-exporter \
    "--web.listen-address=${TAILSCALE_IP}:${PORT}" \
    --collector.cgroups \
    --collector.systemd \
    --collector.textfile \
    --collector.textfile.directory=/var/lib/prometheus/node-exporter \
    "--collector.netdev.device-exclude=^ifb" \
    "--collector.netclass.ignored-devices=^(tap|ifb)" \
    --no-collector.zfs \
    --no-collector.infiniband \
    --no-collector.schedstat
