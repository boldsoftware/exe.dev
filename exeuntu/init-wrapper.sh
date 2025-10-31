#!/bin/bash
#
# Wraps systemd init with two important precursor commands.
#
# Generally, "--privileged" is enough to run; you'll see more
# output with
# 	docker run -it ghcr.io/boldsoftware/exeuntu:latest

echo "Docker users can use Ctrl-P Ctrl-Q to detach."

if [[ $$ != 1 ]]; then
    # There's a really opaque error about telinit otherwise...
    echo "Must be run as pid 1 for systemd to start."
    exit 1
fi

mkdir -p /run/systemd
if [[ ! -f /sys/fs/cgroup/cgroup.controllers ]]; then
    mount -t cgroup2 none /sys/fs/cgroup
fi

if [[ -w /proc/sys/net/ipv4/ip_unprivileged_port_start ]]; then
    printf '0\n' >/proc/sys/net/ipv4/ip_unprivileged_port_start
fi
if [[ -w /proc/sys/net/ipv6/ip_unprivileged_port_start ]]; then
    printf '0\n' >/proc/sys/net/ipv6/ip_unprivileged_port_start
fi

# Kata containers default to mounting this readonly, but Docker needs
# to write to /proc/sys to set net.ipv4.ip_forward=1
mount -o remount,rw /proc/sys

echo "Starting systemd..."
# Add --log-level=debug to see more systemd debugging
exec /sbin/init --log-target=syslog --show-status=true
