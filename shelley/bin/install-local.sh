#!/bin/bash
set -e
trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR

cd "$(dirname "$0")/.."
make build
[[ -f /usr/local/bin/shelley ]] && sudo mv /usr/local/bin/shelley /usr/local/bin/shelley.old
sudo mv bin/shelley /usr/local/bin/shelley
sudo systemctl restart shelley
echo "Shelley installed and restarted."
