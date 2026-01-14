#!/bin/bash
set -e

host="$1"
[[ -z "$host" ]] && {
    echo "usage: $0 <hostname>"
    exit 1
}
[[ "$host" != *.* ]] && host="$host.exe.xyz"

make build-linux-x86
cat bin/shelley-linux-x86 | ssh "$host" "sudo mv /usr/local/bin/shelley /usr/local/bin/shelley.old; sudo tee /usr/local/bin/shelley > /dev/null && sudo chmod +x /usr/local/bin/shelley && sudo systemctl restart shelley"
