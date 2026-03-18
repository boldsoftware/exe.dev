#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "==> Building frontend..."
cd "$SCRIPT_DIR/ui"
npm ci
node build.js
echo "    Frontend built successfully."

echo "==> Installing systemd service..."
sudo cp "$SCRIPT_DIR/bored.service" /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now bored
echo "    Service installed and started."

echo "==> Done. Check status with: journalctl -u bored -f"
