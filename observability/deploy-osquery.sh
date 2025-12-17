#!/bin/bash
set -euo pipefail

# Hosts to deploy osquery to
HOSTS=(
    "exe-ctr-staging-01"
    # Add more hosts here as needed:
    # "exe-ctr-prod-01"
    # "exe-ctr-prod-02"
)

deploy_osquery() {
    local host="$1"
    echo "=== Deploying osquery to $host ==="

    ssh "ubuntu@$host" "bash -s" <<'EOF'
set -euo pipefail

# Create keyrings directory
sudo mkdir -p /etc/apt/keyrings

# Add osquery GPG key
curl -fsSL https://pkg.osquery.io/deb/pubkey.gpg | sudo tee /etc/apt/keyrings/osquery.asc > /dev/null

# Add osquery repository
echo 'deb [arch=amd64 signed-by=/etc/apt/keyrings/osquery.asc] https://pkg.osquery.io/deb deb main' | sudo tee /etc/apt/sources.list.d/osquery.list > /dev/null

# Update and install
sudo apt-get update
sudo apt-get install -y osquery

# Enable and start osqueryd
sudo systemctl enable osqueryd
sudo systemctl start osqueryd

echo "osquery installed successfully"
osqueryi --version
EOF

    echo "=== Done with $host ==="
    echo
}

for host in "${HOSTS[@]}"; do
    deploy_osquery "$host"
done

echo "Deployment complete!"
