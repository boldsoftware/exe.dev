#!/bin/bash
# Provision a NetActuate VM as an exeprox host.
# Run this AFTER the netactuate ansible playbook has created the VM and configured BGP/BIRD.
# Installs Tailscale, builds and deploys sshpiper, and configures the server.
#
# The VM must already be reachable via SSH as ubuntu@<ip>.

set -euo pipefail

usage() {
    cat <<EOF
Usage: $0 <machine-name> <ip>

Provision a NetActuate VM as an exeprox host.
The VM must already exist and have BGP/BIRD configured via the netactuate ansible playbook.

Arguments:
  machine-name   Tailscale hostname for this machine (e.g. exeprox-lax-na-01)
  ip             Public IPv4 address of the VM (from netactuate host_vars)

Example:
  $0 exeprox-lax-na-01 203.0.113.42

Required environment variables:
  TS_OAUTH_CLIENT_ID        Tailscale OAuth client ID
  TS_OAUTH_CLIENT_SECRET    Tailscale OAuth client secret
  HOST_PRIVATE_KEY          SSH host private key
  HOST_CERT_SIG             SSH host certificate signature

To get HOST_PRIVATE_KEY and HOST_CERT_SIG, run:
  ssh exed-02 sqlite3 /home/ubuntu/exe.db "SELECT private_key FROM ssh_host_key WHERE id = 1;"
  ssh exed-02 sqlite3 /home/ubuntu/exe.db "SELECT cert_sig FROM ssh_host_key WHERE id = 1;"

To get the IP from netactuate host_vars after running the playbook:
  grep ansible_ssh_host ops/netactuate/host_vars/<node-name>
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    usage
    exit 0
fi

if [ $# -ne 2 ] || [ -z "$1" ] || [ -z "$2" ]; then
    echo "ERROR: Machine name and IP must be specified" >&2
    echo "" >&2
    usage >&2
    exit 1
fi

MACHINE_NAME="$1"
SERVER_IP="$2"

if [ -z "${TS_OAUTH_CLIENT_ID:-}" ] || [ -z "${TS_OAUTH_CLIENT_SECRET:-}" ]; then
    echo "ERROR: Tailscale OAuth credentials not set" >&2
    echo "Please set the following environment variables:" >&2
    echo "  export TS_OAUTH_CLIENT_ID=<your-client-id>" >&2
    echo "  export TS_OAUTH_CLIENT_SECRET=<your-client-secret>" >&2
    echo "" >&2
    echo "You can get these credentials from the Tailscale admin console:" >&2
    echo "  https://login.tailscale.com/admin/settings/oauth" >&2
    exit 1
fi

if [[ -z "${HOST_PRIVATE_KEY:-}" ]] || [[ -z "${HOST_CERT_SIG:-}" ]]; then
    echo "ERROR: HOST_PRIVATE_KEY and/or HOST_CERT_SIG not set" >&2
    echo "" >&2
    echo 'export HOST_PRIVATE_KEY="$(ssh exed-02 '\''sqlite3 /home/ubuntu/exe.db "SELECT private_key FROM ssh_host_key WHERE id = 1"'\'')"' >&2
    echo 'export HOST_CERT_SIG="$(ssh exed-02 '\''sqlite3 /home/ubuntu/exe.db "SELECT cert_sig FROM ssh_host_key WHERE id = 1"'\'')"' >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# SSH options for direct IP access (netactuate VMs use ubuntu user)
BASE_SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
if [ -n "${SSH_KEY:-}" ]; then
    if [ ! -f "$SSH_KEY" ]; then
        echo "ERROR: SSH key file not found: $SSH_KEY" >&2
        exit 1
    fi
    DIRECT_SSH_OPTS="-i $SSH_KEY $BASE_SSH_OPTS"
else
    DIRECT_SSH_OPTS="$BASE_SSH_OPTS"
fi

# SSH options for Tailscale access
TS_SSH_OPTS="$BASE_SSH_OPTS"

# Run the Tailscale OAuth preflight check
"${SCRIPT_DIR}/test-tailscale-oauth.sh"

echo ""
echo "=========================================="
echo "Provisioning NetActuate exeprox host: ${MACHINE_NAME}"
echo "=========================================="
echo "IP: ${SERVER_IP}"
echo ""

# Wait for SSH to become available
echo "Waiting for SSH to become available..."
MAX_SSH_WAIT=300
SSH_ELAPSED=0

while [ $SSH_ELAPSED -lt $MAX_SSH_WAIT ]; do
    if ssh -o ConnectTimeout=5 $DIRECT_SSH_OPTS \
        "ubuntu@${SERVER_IP}" true 2>/dev/null; then
        echo "SSH is available"
        break
    fi
    echo "  Waiting for SSH... ($SSH_ELAPSED/$MAX_SSH_WAIT seconds)"
    sleep 10
    SSH_ELAPSED=$((SSH_ELAPSED + 10))
done

if [ $SSH_ELAPSED -ge $MAX_SSH_WAIT ]; then
    echo "ERROR: SSH not available after ${MAX_SSH_WAIT} seconds"
    exit 1
fi

echo ""
echo "Running apt-get update and upgrade..."
ssh $DIRECT_SSH_OPTS \
    "ubuntu@${SERVER_IP}" 'sudo DEBIAN_FRONTEND=noninteractive apt-get update && sudo DEBIAN_FRONTEND=noninteractive apt-get upgrade -y'

echo ""
echo "Installing dependencies..."
ssh $DIRECT_SSH_OPTS \
    "ubuntu@${SERVER_IP}" 'sudo DEBIAN_FRONTEND=noninteractive apt-get install -y curl jq'

echo ""
echo "Installing ghostty terminfo..."
infocmp -x xterm-ghostty 2>/dev/null | ssh $DIRECT_SSH_OPTS \
    "ubuntu@${SERVER_IP}" 'tic -x -' || echo "  (ghostty terminfo not available locally, skipping)"

echo ""
echo "Installing and configuring Tailscale..."

# Create the tailscale setup script
cat <<'TAILSCALE_SETUP' | ssh $DIRECT_SSH_OPTS "ubuntu@${SERVER_IP}" "cat > /tmp/setup-tailscale.sh"
#!/bin/bash
set -euo pipefail

HOSTNAME="$1"
TS_OAUTH_CLIENT_ID="$2"
TS_OAUTH_CLIENT_SECRET="$3"

echo "Installing Tailscale..."
curl -fsSL https://tailscale.com/install.sh | sudo sh

echo "Generating Tailscale auth key via OAuth..."
OAUTH_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    "https://api.tailscale.com/api/v2/oauth/token" \
    -d "client_id=${TS_OAUTH_CLIENT_ID}" \
    -d "client_secret=${TS_OAUTH_CLIENT_SECRET}" \
    -d "grant_type=client_credentials")

OAUTH_HTTP=$(echo "$OAUTH_RESPONSE" | tail -n 1)
OAUTH_BODY=$(echo "$OAUTH_RESPONSE" | head -n -1)

if [ "$OAUTH_HTTP" != "200" ]; then
    echo "ERROR: Failed to get OAuth token. HTTP code: $OAUTH_HTTP"
    echo "Response body: $OAUTH_BODY"
    exit 1
fi

ACCESS_TOKEN=$(echo "$OAUTH_BODY" | jq -r '.access_token')
if [ -z "$ACCESS_TOKEN" ] || [ "$ACCESS_TOKEN" = "null" ]; then
    echo "ERROR: Failed to extract access token"
    echo "Response body: $OAUTH_BODY"
    exit 1
fi

echo "Creating Tailscale auth key..."
KEY_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    "https://api.tailscale.com/api/v2/tailnet/-/keys" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{
        "capabilities": {
            "devices": {
                "create": {
                    "reusable": false,
                    "ephemeral": false,
                    "tags": ["tag:server"]
                }
            }
        },
        "expirySeconds": 3600
    }')

KEY_HTTP=$(echo "$KEY_RESPONSE" | tail -n 1)
KEY_BODY=$(echo "$KEY_RESPONSE" | head -n -1)

if [ "$KEY_HTTP" != "200" ]; then
    echo "ERROR: Failed to create auth key. HTTP code: $KEY_HTTP"
    echo "Response body: $KEY_BODY"
    exit 1
fi

AUTH_KEY=$(echo "$KEY_BODY" | jq -r '.key')
if [ -z "$AUTH_KEY" ] || [ "$AUTH_KEY" = "null" ]; then
    echo "ERROR: Failed to extract auth key from response"
    echo "Response body: $KEY_BODY"
    exit 1
fi

echo "Starting Tailscale with hostname: ${HOSTNAME}"
sudo tailscale up --authkey="$AUTH_KEY" --advertise-tags=tag:server --ssh --hostname="${HOSTNAME}"
echo "Tailscale up completed"
sleep 5
sudo tailscale status
TAILSCALE_SETUP

# Execute the tailscale setup script
ssh $DIRECT_SSH_OPTS \
    "ubuntu@${SERVER_IP}" "chmod +x /tmp/setup-tailscale.sh && /tmp/setup-tailscale.sh '${MACHINE_NAME}' '${TS_OAUTH_CLIENT_ID}' '${TS_OAUTH_CLIENT_SECRET}'"

# Wait for Tailscale to be accessible
echo ""
echo "Waiting for Tailscale SSH to be accessible..."
MAX_TS_WAIT=120
TS_ELAPSED=0

while [ $TS_ELAPSED -lt $MAX_TS_WAIT ]; do
    if ssh -o ConnectTimeout=5 $TS_SSH_OPTS \
        "ubuntu@${MACHINE_NAME}" true 2>/dev/null; then
        echo "Machine is accessible via Tailscale SSH"
        break
    fi
    echo "  Waiting for Tailscale... ($TS_ELAPSED/$MAX_TS_WAIT seconds)"
    sleep 10
    TS_ELAPSED=$((TS_ELAPSED + 10))
done

if [ $TS_ELAPSED -ge $MAX_TS_WAIT ]; then
    echo "WARNING: Machine is not accessible via Tailscale after ${MAX_TS_WAIT} seconds"
    echo "You may need to check the Tailscale setup manually"
    echo "Direct SSH: ssh ubuntu@${SERVER_IP}"
    exit 1
fi

# Now use Tailscale SSH for the rest of setup
SSH_TARGET="ubuntu@${MACHINE_NAME}"

# Disable OpenSSH server now that Tailscale SSH is working
echo ""
echo "Disabling OpenSSH server (sshpiper will use port 22)..."
ssh $TS_SSH_OPTS "${SSH_TARGET}" \
    'sudo systemctl disable ssh ssh.socket && sudo systemctl stop ssh ssh.socket'
echo "OpenSSH server disabled"

echo ""
echo "=========================================="
echo "Building sshpiper binaries"
echo "=========================================="

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BINARY_NAME="sshpiperd.$TIMESTAMP"
METRICS_NAME="metrics.$TIMESTAMP"

echo "Building sshpiper binary..."
(
    cd "${SCRIPT_DIR}/../../deps/sshpiper"
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$BINARY_NAME" ./cmd/sshpiperd
)

if [ ! -f "/tmp/$BINARY_NAME" ]; then
    echo "ERROR: Failed to build sshpiper binary"
    exit 1
fi
echo "Built /tmp/$BINARY_NAME"

echo "Building metrics binary..."
(
    cd "${SCRIPT_DIR}/../../deps/sshpiper"
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "/tmp/$METRICS_NAME" ./plugin/metrics
)

if [ ! -f "/tmp/$METRICS_NAME" ]; then
    echo "ERROR: Failed to build metrics binary"
    exit 1
fi
echo "Built /tmp/$METRICS_NAME"

echo ""
echo "=========================================="
echo "Deploying sshpiper to ${MACHINE_NAME}"
echo "=========================================="

# Copy binaries
echo "Copying binaries..."
scp $TS_SSH_OPTS \
    "/tmp/$BINARY_NAME" "/tmp/$METRICS_NAME" \
    "${SSH_TARGET}:~/"

# Copy start script and service file
echo "Copying start script and service file..."
scp $TS_SSH_OPTS \
    "${SCRIPT_DIR}/exeprox-sshpiper.sh" \
    "${SCRIPT_DIR}/exeprox-sshpiper.service" \
    "${SSH_TARGET}:~/"

# Write host key files and configure service
echo "Configuring sshpiper..."
ssh $TS_SSH_OPTS "${SSH_TARGET}" bash -s "$BINARY_NAME" "$METRICS_NAME" <<'CONFIGURE_SSHPIPER'
set -euo pipefail
BINARY_NAME="$1"
METRICS_NAME="$2"

# Rename scripts to standard names
mv ~/exeprox-sshpiper.sh ~/start-sshpiper.sh
mv ~/exeprox-sshpiper.service ~/sshpiper.service

# Make binaries executable
chmod +x ~/$BINARY_NAME ~/$METRICS_NAME ~/start-sshpiper.sh

# Create symlinks to latest versions
ln -sf ~/$BINARY_NAME ~/sshpiperd.latest
ln -sf ~/$METRICS_NAME ~/metrics.latest

# Install systemd service file
sudo mv ~/sshpiper.service /etc/systemd/system/sshpiper.service
sudo systemctl daemon-reload
CONFIGURE_SSHPIPER

# Write the host key files (done separately to handle special characters)
echo "Writing host key files..."
printf '%s' "$HOST_PRIVATE_KEY" | ssh $TS_SSH_OPTS \
    "${SSH_TARGET}" "cat > ~/host_private_key && chmod 600 ~/host_private_key"

printf '%s' "$HOST_CERT_SIG" | ssh $TS_SSH_OPTS \
    "${SSH_TARGET}" "cat > ~/host_cert_sig && chmod 600 ~/host_cert_sig"

# Enable and start the service
echo "Starting sshpiper service..."
ssh $TS_SSH_OPTS "${SSH_TARGET}" bash <<'START_SERVICE'
set -euo pipefail
sudo systemctl enable sshpiper
sudo systemctl restart sshpiper

# Wait and check status
sleep 2
if sudo systemctl is-active --quiet sshpiper; then
    echo "sshpiper service started successfully"
else
    echo "WARNING: sshpiper service may have issues"
    sudo journalctl -u sshpiper -n 20 --no-pager
fi
START_SERVICE

# Cleanup
rm -f "/tmp/$BINARY_NAME" "/tmp/$METRICS_NAME"

echo ""
echo "=========================================="
echo "Installing node_exporter for monitoring"
echo "=========================================="

ssh $TS_SSH_OPTS "${SSH_TARGET}" 'bash -s' <<'NODE_EXPORTER_SCRIPT'
set -euo pipefail
if ! dpkg -l | grep -q prometheus-node-exporter; then
    echo "Installing prometheus-node-exporter..."
    sudo apt-get update && sudo apt-get install -y prometheus-node-exporter
else
    echo "prometheus-node-exporter already installed"
fi

# Create wrapper script that dynamically gets Tailscale IP at start time
cat <<'WRAPPER' | sudo tee /usr/local/bin/node-exporter-wrapper > /dev/null
#!/bin/bash
TAILSCALE_IP=$(tailscale ip -4)
if [ -z "$TAILSCALE_IP" ]; then
    echo "ERROR: Failed to get Tailscale IP" >&2
    exit 1
fi
exec /usr/bin/prometheus-node-exporter --web.listen-address=${TAILSCALE_IP}:19100 --collector.systemd "$@"
WRAPPER
sudo chmod +x /usr/local/bin/node-exporter-wrapper

sudo mkdir -p /etc/systemd/system/prometheus-node-exporter.service.d
cat <<EOF | sudo tee /etc/systemd/system/prometheus-node-exporter.service.d/override.conf > /dev/null
[Unit]
After=tailscaled.service
Wants=tailscaled.service

[Service]
ExecStart=
ExecStart=/usr/local/bin/node-exporter-wrapper
EOF
sudo systemctl daemon-reload
sudo systemctl enable prometheus-node-exporter
sudo systemctl restart prometheus-node-exporter

TAILSCALE_IP=$(tailscale ip -4)
echo "node_exporter should be listening on Tailscale IP: $TAILSCALE_IP"
echo "Verifying node-exporter is running..."
for i in $(seq 1 300); do
    if curl -sf -o /dev/null --max-time 2 http://${TAILSCALE_IP}:19100/metrics; then
        echo "node-exporter is responding on ${TAILSCALE_IP}:19100"
        break
    fi
    if [ $i -eq 300 ]; then
        echo "ERROR: node-exporter failed to start after 30 seconds"
        exit 1
    fi
    sleep 0.1
done
NODE_EXPORTER_SCRIPT

echo ""
echo "=========================================="
echo "Provisioning complete!"
echo "=========================================="
echo ""
echo "  Hostname: ${MACHINE_NAME}"
echo "  Public IP: ${SERVER_IP}"
echo ""
echo "Connect via Tailscale:"
echo "  ssh ubuntu@${MACHINE_NAME}"
echo ""
echo "Next step: deploy exeprox with deploy-exeprox-netactuate.sh"
echo "  ops/deploy/deploy-exeprox-netactuate.sh ${MACHINE_NAME}"
echo "=========================================="
