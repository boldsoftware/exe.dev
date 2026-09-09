#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "usage: sudo $0 PATH-TO-EXECONNECT" >&2
    exit 2
fi

binary=$1
case $0 in
*/*) script_parent=${0%/*} ;;
*) script_parent=. ;;
esac
script_dir=$(CDPATH= cd "$script_parent" && pwd)
unit=$script_dir/execonnect.service

if [ ! -f "$unit" ] || [ ! -r "$unit" ]; then
    echo "systemd unit must be a readable file: $unit" >&2
    exit 1
fi
if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
    echo "execonnect binary must be an executable file: $binary" >&2
    exit 1
fi

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "required command not found: $1" >&2
        exit 1
    fi
}

for command_name in id getent install systemctl; do
    require_command "$command_name"
done
if [ "$(id -u)" -ne 0 ]; then
    echo "install.sh must run as root" >&2
    exit 1
fi

if getent group execonnect >/dev/null 2>&1; then
    group_exists=true
else
    group_exists=false
fi
if getent passwd execonnect >/dev/null 2>&1; then
    user_exists=true
else
    user_exists=false
fi

if [ "$user_exists" = true ]; then
    if [ "$group_exists" != true ] || [ "$(id -gn execonnect)" != "execonnect" ]; then
        echo "existing execonnect user must have execonnect as its primary group" >&2
        exit 1
    fi
fi
if [ "$group_exists" != true ]; then
    require_command groupadd
fi
if [ "$user_exists" != true ]; then
    require_command useradd
fi

if [ "$group_exists" != true ]; then
    groupadd --system execonnect
fi
if [ "$user_exists" != true ]; then
    useradd --system --gid execonnect --home-dir /var/lib/execonnect --no-create-home --shell /usr/sbin/nologin execonnect
fi

install -m 0755 "$binary" /usr/local/bin/execonnect
install -m 0644 "$unit" /etc/systemd/system/execonnect.service
systemctl daemon-reload

echo "Installed execonnect; run: systemctl enable --now execonnect"
