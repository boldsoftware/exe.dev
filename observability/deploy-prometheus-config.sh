#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Deploying exelet discovery script and timer..."
scp "${SCRIPT_DIR}/scripts/discover-exelets.py" ubuntu@mon:/tmp/discover-exelets.py
ssh ubuntu@mon "sudo mv /tmp/discover-exelets.py /usr/local/bin/discover-exelets.py && sudo chmod 755 /usr/local/bin/discover-exelets.py"

scp "${SCRIPT_DIR}/discover-exelets.service" "${SCRIPT_DIR}/discover-exelets.timer" ubuntu@mon:/tmp/
ssh ubuntu@mon "sudo mv /tmp/discover-exelets.service /tmp/discover-exelets.timer /etc/systemd/system/"

# Run discovery once now to seed the target files before prometheus starts
echo "Running initial exelet discovery..."
ssh ubuntu@mon "sudo mkdir -p /etc/prometheus/targets && sudo python3 /usr/local/bin/discover-exelets.py"

# Enable and start the timer
ssh ubuntu@mon "sudo systemctl daemon-reload && sudo systemctl enable --now discover-exelets.timer"

echo "Deploying prometheus config..."
scp "${SCRIPT_DIR}/prometheus.yml" ubuntu@mon:/home/ubuntu/prometheus/prometheus.yml
ssh ubuntu@mon sudo systemctl restart prometheus
