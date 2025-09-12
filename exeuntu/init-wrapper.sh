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
echo "Starting systemd..."
exec /sbin/init
