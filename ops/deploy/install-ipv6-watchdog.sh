#!/bin/bash
# Install the IPv6-disable watchdog (script + cron entry) on existing hosts.
#
# Newly-provisioned hosts get this from setup-exelet-host.sh and
# latitude/provision-exelet-host.sh. This companion is for hosts that
# were provisioned before that change landed.
#
# The watchdog runs every 5 min and, if any inet6 address is present,
# schedules a delayed tailscaled restart and re-applies the disable_ipv6
# / accept_ra sysctls.
#
# Usage:
#   install-ipv6-watchdog.sh <host> [<host> ...]
#   echo host1 host2 | install-ipv6-watchdog.sh -
#
# Hosts are expected to be reachable as `ubuntu@<host>` over Tailscale SSH.

set -euo pipefail

usage() {
    cat <<EOF
Usage: $0 <host> [<host> ...]
       $0 -        (read whitespace-separated hosts from stdin)

Installs /usr/local/sbin/disable-ipv6-if-needed.sh and /etc/cron.d/disable-ipv6-watchdog
on each host via Tailscale SSH (ubuntu@<host>).
EOF
}

if [ $# -eq 0 ]; then
    usage >&2
    exit 1
fi

if [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
    usage
    exit 0
fi

HOSTS=()
if [ "$1" = "-" ]; then
    while read -r line; do
        for h in $line; do
            [ -n "$h" ] && HOSTS+=("$h")
        done
    done
else
    HOSTS=("$@")
fi

if [ ${#HOSTS[@]} -eq 0 ]; then
    echo "ERROR: no hosts specified" >&2
    exit 1
fi

SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o BatchMode=yes"

# The remote installer. Runs as ubuntu (uses sudo). Idempotent.
REMOTE_INSTALL=$(
    cat <<'REMOTE'
set -euo pipefail

sudo tee /usr/local/sbin/disable-ipv6-if-needed.sh > /dev/null <<'WATCHDOG'
#!/bin/bash
set -e
if ip -6 addr show 2>/dev/null | grep -q 'inet6'; then
    systemd-run --on-active=30 /bin/systemctl restart tailscaled
    sysctl -w net.ipv6.conf.default.disable_ipv6=1
    sysctl -w net.ipv6.conf.all.disable_ipv6=1
    sysctl -w net.ipv6.conf.all.accept_ra=0
    sysctl -w net.ipv6.conf.default.accept_ra=0
fi
WATCHDOG
sudo chmod 0755 /usr/local/sbin/disable-ipv6-if-needed.sh

sudo tee /etc/cron.d/disable-ipv6-watchdog > /dev/null <<'CRON'
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
*/5 * * * * root /usr/local/sbin/disable-ipv6-if-needed.sh >/dev/null 2>&1
CRON
sudo chmod 0644 /etc/cron.d/disable-ipv6-watchdog

# cron.d entries are picked up automatically; nudge cron to re-read just in case.
sudo systemctl reload cron 2>/dev/null || sudo systemctl restart cron 2>/dev/null || true

echo "installed: /usr/local/sbin/disable-ipv6-if-needed.sh"
echo "installed: /etc/cron.d/disable-ipv6-watchdog"
REMOTE
)

install_one() {
    local host="$1"
    if ! ssh $SSH_OPTS "ubuntu@$host" "$REMOTE_INSTALL"; then
        return 1
    fi
}

LOGDIR=$(mktemp -d)
PIDS=()
HOSTNAMES=()

for host in "${HOSTS[@]}"; do
    echo "Starting: $host"
    install_one "$host" >"$LOGDIR/$host.log" 2>&1 &
    PIDS+=($!)
    HOSTNAMES+=("$host")
done

echo ""
echo "Launched ${#HOSTS[@]} install(s). Waiting..."
echo ""

FAILED=0
for i in "${!PIDS[@]}"; do
    pid=${PIDS[$i]}
    host=${HOSTNAMES[$i]}
    if wait "$pid"; then
        echo "✓ $host"
    else
        echo "✗ $host FAILED (see $LOGDIR/$host.log)"
        FAILED=$((FAILED + 1))
    fi
done

echo ""
if [ "$FAILED" -eq 0 ]; then
    echo "All ${#HOSTS[@]} hosts updated."
    rm -rf "$LOGDIR"
else
    echo "$FAILED/${#HOSTS[@]} failed. Logs in $LOGDIR"
    exit 1
fi
