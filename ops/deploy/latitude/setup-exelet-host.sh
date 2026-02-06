#!/bin/bash
# Create an exelet host on Latitude.sh for production
# This script provisions the server, waits for it to come online, and then
# configures it via SSH.
#
# For servers with multiple large NVMe drives (>1TB), this script:
# - Creates a 2TB swap partition on each large NVMe drive
# - Creates a raidz1 ZFS pool from the remaining space on each drive

set -e

usage() {
    cat <<EOF
Usage: $0 <hostname>

Create a bare metal exelet host on Latitude.sh for production.
If a server with the hostname already exists, prompts to re-provision via SSH.

For servers with multiple NVMe drives larger than 1TB:
  - Creates a 2TB swap partition on each drive
  - Uses the remaining space on each drive for a raidz1 ZFS pool named "tank"

Example:
  $0 exe-prod-01

Required environment variables:
  LATITUDE_API_KEY          Latitude.sh API key
  LATITUDE_PROJECT          Latitude.sh project ID or slug
  TS_OAUTH_CLIENT_ID        Tailscale OAuth client ID
  TS_OAUTH_CLIENT_SECRET    Tailscale OAuth client secret

Optional environment variables:
  LATITUDE_SITE             Latitude.sh site (default: LAX2)
  LATITUDE_PLAN             Server plan (default: rs4-metal-xlarge)
  SSH_KEY                   Path to SSH private key for direct IP access

Get Tailscale OAuth credentials from:
  https://login.tailscale.com/admin/settings/oauth
EOF
}

if [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
    usage
    exit 0
fi

if [ $# -ne 1 ] || [ -z "$1" ]; then
    echo "ERROR: Server hostname must be specified" >&2
    echo "" >&2
    usage >&2
    exit 1
fi

HOSTNAME="$1"

if [ -z "$LATITUDE_API_KEY" ]; then
    echo "ERROR: LATITUDE_API_KEY environment variable is not set" >&2
    exit 1
fi

if [ -z "$LATITUDE_PROJECT" ]; then
    echo "ERROR: LATITUDE_PROJECT environment variable is not set" >&2
    exit 1
fi

if [ -z "$TS_OAUTH_CLIENT_ID" ] || [ -z "$TS_OAUTH_CLIENT_SECRET" ]; then
    echo "ERROR: Tailscale OAuth credentials not set" >&2
    echo "Please set the following environment variables:" >&2
    echo "  export TS_OAUTH_CLIENT_ID=<your-client-id>" >&2
    echo "  export TS_OAUTH_CLIENT_SECRET=<your-client-secret>" >&2
    echo "" >&2
    echo "You can get these credentials from the Tailscale admin console:" >&2
    echo "  https://login.tailscale.com/admin/settings/oauth" >&2
    exit 1
fi

# Configuration
PROJECT="$LATITUDE_PROJECT"
SITE="${LATITUDE_SITE:-LAX2}"
PLAN="${LATITUDE_PLAN:-rs4-metal-xlarge}"
# Latitude website shows plans with dots, but API requires hyphens
PLAN="${PLAN//./-}"
OS="ubuntu_24_04_x64_lts"
SSH_TIMEOUT=1800 # 30 minutes
BASE_SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o BatchMode=yes"

# SSH options for Tailscale access (no key needed)
SSH_OPTS="$BASE_SSH_OPTS"

# SSH options for direct IP access (with key if provided)
if [ -n "${SSH_KEY:-}" ]; then
    if [ ! -f "$SSH_KEY" ]; then
        echo "ERROR: SSH key file not found: $SSH_KEY" >&2
        exit 1
    fi
    DIRECT_SSH_OPTS="-i $SSH_KEY $BASE_SSH_OPTS"
else
    DIRECT_SSH_OPTS="$BASE_SSH_OPTS"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STANDALONE_DIR="$SCRIPT_DIR/../standalone"

# Verify standalone scripts exist
for script in create-exelet-standalone.sh setup-iptables-exelet.sh; do
    if [ ! -f "$STANDALONE_DIR/$script" ]; then
        echo "ERROR: Missing $STANDALONE_DIR/$script" >&2
        exit 1
    fi
done

# Copy a file to remote server via SSH
copy_to_remote() {
    local src="$1"
    local dst="$2"
    local target="$3"
    cat "$src" | ssh $SSH_OPTS "$target" "cat > $dst && chmod +x $dst"
}

# Setup Tailscale on the server (run via direct SSH to IP)
setup_tailscale() {
    local target="$1"
    local hostname="$2"

    echo "Installing Tailscale..."
    ssh $DIRECT_SSH_OPTS "$target" 'sudo DEBIAN_FRONTEND=noninteractive apt-get update && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y curl jq pv'
    ssh $DIRECT_SSH_OPTS "$target" 'curl -fsSL https://tailscale.com/install.sh | sudo sh'

    echo "Generating Tailscale auth key via OAuth..."
    # Create the tailscale setup script
    cat <<TAILSCALE_SETUP | ssh $DIRECT_SSH_OPTS "$target" "cat > /tmp/setup-tailscale.sh && chmod +x /tmp/setup-tailscale.sh"
#!/bin/bash
set -euo pipefail

HOSTNAME="$hostname"
TS_OAUTH_CLIENT_ID="$TS_OAUTH_CLIENT_ID"
TS_OAUTH_CLIENT_SECRET="$TS_OAUTH_CLIENT_SECRET"

echo "Generating Tailscale auth key via OAuth..."
OAUTH_RESPONSE=\$(curl -s -w "\n%{http_code}" -X POST \\
    "https://api.tailscale.com/api/v2/oauth/token" \\
    -d "client_id=\${TS_OAUTH_CLIENT_ID}" \\
    -d "client_secret=\${TS_OAUTH_CLIENT_SECRET}" \\
    -d "grant_type=client_credentials")

OAUTH_HTTP=\$(echo "\$OAUTH_RESPONSE" | tail -n 1)
OAUTH_BODY=\$(echo "\$OAUTH_RESPONSE" | head -n -1)

if [ "\$OAUTH_HTTP" != "200" ]; then
    echo "ERROR: Failed to get OAuth token. HTTP code: \$OAUTH_HTTP"
    echo "Response body: \$OAUTH_BODY"
    exit 1
fi

ACCESS_TOKEN=\$(echo "\$OAUTH_BODY" | jq -r '.access_token')
if [ -z "\$ACCESS_TOKEN" ] || [ "\$ACCESS_TOKEN" = "null" ]; then
    echo "ERROR: Failed to extract access token"
    echo "Response body: \$OAUTH_BODY"
    exit 1
fi

echo "Creating Tailscale auth key..."
KEY_RESPONSE=\$(curl -s -w "\n%{http_code}" -X POST \\
    "https://api.tailscale.com/api/v2/tailnet/-/keys" \\
    -H "Authorization: Bearer \$ACCESS_TOKEN" \\
    -H "Content-Type: application/json" \\
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

KEY_HTTP=\$(echo "\$KEY_RESPONSE" | tail -n 1)
KEY_BODY=\$(echo "\$KEY_RESPONSE" | head -n -1)

if [ "\$KEY_HTTP" != "200" ]; then
    echo "ERROR: Failed to create auth key. HTTP code: \$KEY_HTTP"
    echo "Response body: \$KEY_BODY"
    exit 1
fi

AUTH_KEY=\$(echo "\$KEY_BODY" | jq -r '.key')
if [ -z "\$AUTH_KEY" ] || [ "\$AUTH_KEY" = "null" ]; then
    echo "ERROR: Failed to extract auth key from response"
    echo "Response body: \$KEY_BODY"
    exit 1
fi

echo "Starting Tailscale with hostname: \${HOSTNAME}"
sudo tailscale up --authkey="\$AUTH_KEY" --advertise-tags=tag:server --ssh --hostname="\${HOSTNAME}"
echo "Tailscale up completed"
sleep 5
sudo tailscale status
TAILSCALE_SETUP

    ssh $DIRECT_SSH_OPTS "$target" "/tmp/setup-tailscale.sh && rm -f /tmp/setup-tailscale.sh"
}

# Setup NVMe drives with swap and raidz1
# This function runs on the remote server
setup_nvme_drives_script() {
    cat <<'NVME_SCRIPT'
#!/bin/bash
set -euo pipefail

SWAP_SIZE_GB=2048  # 2TB swap per drive
MIN_DRIVE_SIZE_GB=1024  # Only use drives larger than 1TB

echo "=== Installing required packages ==="
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y gdisk parted zfsutils-linux

echo "=== Setting up NVMe drives with swap and raidz1 ==="

# Find all NVMe drives larger than 1TB (excluding partitions)
LARGE_NVME_DRIVES=()
for dev in /dev/nvme*n1; do
    [ -b "$dev" ] || continue

    # Get size in bytes
    size_bytes=$(blockdev --getsize64 "$dev" 2>/dev/null || echo 0)
    size_gb=$((size_bytes / 1024 / 1024 / 1024))

    if [ "$size_gb" -gt "$MIN_DRIVE_SIZE_GB" ]; then
        echo "Found large NVMe drive: $dev (${size_gb}GB)"
        LARGE_NVME_DRIVES+=("$dev")
    else
        echo "Skipping small NVMe drive: $dev (${size_gb}GB)"
    fi
done

if [ ${#LARGE_NVME_DRIVES[@]} -eq 0 ]; then
    echo "No NVMe drives larger than ${MIN_DRIVE_SIZE_GB}GB found"
    echo "Skipping swap and ZFS raidz1 setup"
    mkdir -p /data/exelet
    exit 0
fi

echo ""
echo "Will configure ${#LARGE_NVME_DRIVES[@]} NVMe drive(s):"
printf '  %s\n' "${LARGE_NVME_DRIVES[@]}"
echo ""

# Arrays to collect partitions
SWAP_PARTITIONS=()
DATA_PARTITIONS=()

for dev in "${LARGE_NVME_DRIVES[@]}"; do
    echo "=== Partitioning $dev ==="

    # Check if already has ZFS
    fs_type="$(blkid -o value -s TYPE "$dev" 2>/dev/null || true)"
    if [ "$fs_type" = "zfs_member" ]; then
        echo "WARNING: $dev is already a ZFS member, skipping partitioning"
        continue
    fi

    # Check for existing partitions that might be swap or ZFS
    part1="${dev}p1"
    part2="${dev}p2"

    # If partitions already exist with correct layout, just use them
    if [ -b "$part1" ] && [ -b "$part2" ]; then
        part1_type="$(blkid -o value -s TYPE "$part1" 2>/dev/null || true)"
        part2_type="$(blkid -o value -s TYPE "$part2" 2>/dev/null || true)"

        if [ "$part1_type" = "swap" ] && [ "$part2_type" = "zfs_member" ]; then
            echo "Partitions already configured correctly on $dev"
            SWAP_PARTITIONS+=("$part1")
            DATA_PARTITIONS+=("$part2")
            continue
        fi
    fi

    # Unmount and disable any existing swap
    swapoff "$dev"* 2>/dev/null || true

    # Wipe existing filesystem signatures
    echo "Wiping existing signatures on $dev..."
    wipefs -af "$dev" >/dev/null 2>&1 || true
    for p in "${dev}p"*; do
        [ -b "$p" ] && wipefs -af "$p" >/dev/null 2>&1 || true
    done

    # Clear partition table
    sgdisk --zap-all "$dev" >/dev/null 2>&1 || true

    # Zero out first and last MB
    dd if=/dev/zero of="$dev" bs=1M count=1 2>/dev/null || true
    size_sectors=$(blockdev --getsz "$dev")
    dd if=/dev/zero of="$dev" bs=1M seek=$((size_sectors / 2048 - 1)) count=1 2>/dev/null || true

    # Inform kernel of changes
    partprobe "$dev" 2>/dev/null || true
    sleep 1

    # Create partitions:
    # Partition 1: 2TB for swap
    # Partition 2: remainder for ZFS
    echo "Creating partitions on $dev..."
    sgdisk -n 1:0:+${SWAP_SIZE_GB}G -t 1:8200 -c 1:"swap" "$dev"
    sgdisk -n 2:0:0 -t 2:BF00 -c 2:"zfs-data" "$dev"

    # Inform kernel of changes
    partprobe "$dev" 2>/dev/null || true
    sleep 1

    # Verify partitions exist
    if [ ! -b "$part1" ] || [ ! -b "$part2" ]; then
        echo "ERROR: Partitions not created on $dev" >&2
        exit 1
    fi

    SWAP_PARTITIONS+=("$part1")
    DATA_PARTITIONS+=("$part2")
done

echo ""
echo "=== Setting up swap partitions ==="

# Disable and remove swap.img if present
if [ -f /swap.img ]; then
    echo "Disabling and removing /swap.img..."
    swapoff /swap.img 2>/dev/null || true
    rm -f /swap.img
fi
# Remove swap.img from fstab
sed -i '/\/swap.img/d' /etc/fstab

# Remove all existing swap entries from fstab (we'll add fresh ones)
echo "Cleaning up old swap entries from /etc/fstab..."
sed -i '/none.*swap.*sw/d' /etc/fstab

for part in "${SWAP_PARTITIONS[@]}"; do
    echo "Formatting $part as swap..."
    mkswap -L "swap-$(basename "$part")" "$part"

    # Add to fstab with priority 1 (equal priority = kernel stripes across all)
    part_uuid=$(blkid -s UUID -o value "$part")
    echo "UUID=$part_uuid none swap sw,pri=1 0 0" >> /etc/fstab
    echo "Added $part (UUID=$part_uuid) to /etc/fstab with pri=1"

    # Enable swap with priority 1
    swapon -p 1 "$part"
    echo "Enabled swap on $part with priority 1"
done

echo ""
echo "=== Setting up ZFS raidz1 pool ==="

if [ ${#DATA_PARTITIONS[@]} -eq 0 ]; then
    echo "No data partitions available for ZFS"
    mkdir -p /data/exelet
    exit 0
fi

# Check if tank pool already exists
if zpool list tank >/dev/null 2>&1; then
    echo "ZFS pool 'tank' already exists"
    zpool status tank
else
    if [ ${#DATA_PARTITIONS[@]} -eq 1 ]; then
        # Single drive - no redundancy possible
        echo "Creating ZFS pool 'tank' with single drive..."
        zpool create -f -m none tank "${DATA_PARTITIONS[0]}"
    else
        # Multiple drives - use raidz1
        echo "Creating ZFS pool 'tank' with raidz1 using ${#DATA_PARTITIONS[@]} drives..."
        zpool create -f -m none tank raidz1 "${DATA_PARTITIONS[@]}"
    fi

    # Create data dataset
    zfs create -o mountpoint=/data tank/data
fi

mkdir -p /data/exelet

echo ""
echo "=== Disk setup complete ==="
echo "Swap partitions: ${SWAP_PARTITIONS[*]}"
echo "ZFS pool 'tank' with data partitions: ${DATA_PARTITIONS[*]}"
zpool status tank
echo ""
swapon --show
NVME_SCRIPT
}

# Provision server via Tailscale SSH
provision_server() {
    local host="$1"

    # Run NVMe setup (swap + raidz1)
    echo ""
    echo "=== Setting up NVMe drives (swap + raidz1) ==="
    setup_nvme_drives_script | ssh $SSH_OPTS "ubuntu@$host" "cat > /tmp/setup-nvme.sh && chmod +x /tmp/setup-nvme.sh"
    ssh $SSH_OPTS "ubuntu@$host" "sudo /tmp/setup-nvme.sh && rm -f /tmp/setup-nvme.sh"

    # Run create-exelet-standalone.sh with --skip-zfs since we already set it up
    echo ""
    echo "=== Running create-exelet-standalone.sh ==="
    copy_to_remote "$STANDALONE_DIR/create-exelet-standalone.sh" "/tmp/create-exelet-standalone.sh" "ubuntu@$host"
    ssh $SSH_OPTS "ubuntu@$host" "sudo /tmp/create-exelet-standalone.sh --skip-zfs && rm -f /tmp/create-exelet-standalone.sh"

    # Run setup-iptables-exelet.sh
    echo ""
    echo "=== Running setup-iptables-exelet.sh ==="
    copy_to_remote "$STANDALONE_DIR/setup-iptables-exelet.sh" "/tmp/setup-iptables-exelet.sh" "ubuntu@$host"
    ssh $SSH_OPTS "ubuntu@$host" "sudo /tmp/setup-iptables-exelet.sh && rm -f /tmp/setup-iptables-exelet.sh"

    # Install and configure node_exporter for monitoring
    echo ""
    echo "=== Installing node_exporter for monitoring ==="
    ssh $SSH_OPTS "ubuntu@$host" 'bash -s' <<'NODE_EXPORTER_SCRIPT'
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
exec /usr/bin/prometheus-node-exporter --web.listen-address=${TAILSCALE_IP}:9100 --collector.cgroups --collector.systemd "$@"
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
NODE_EXPORTER_SCRIPT
}

# Check if server with this hostname already exists
echo "Checking for existing server with hostname '$HOSTNAME'..."
SERVERS_RESPONSE=$(curl -s "https://api.latitude.sh/servers?filter%5Bproject%5D=$PROJECT" \
    -H "Authorization: Bearer $LATITUDE_API_KEY")

EXISTING_SERVER=$(echo "$SERVERS_RESPONSE" | jq -r "(.data // [])[] | select(.attributes.hostname == \"$HOSTNAME\")")

if [ -n "$EXISTING_SERVER" ]; then
    SERVER_ID=$(echo "$EXISTING_SERVER" | jq -r '.id')
    SERVER_STATUS=$(echo "$EXISTING_SERVER" | jq -r '.attributes.status')
    PUBLIC_IP=$(echo "$EXISTING_SERVER" | jq -r '.attributes.primary_ipv4')

    echo ""
    echo "Found existing server:"
    echo "  Hostname:  $HOSTNAME"
    echo "  Server ID: $SERVER_ID"
    echo "  Status:    $SERVER_STATUS"
    echo "  Public IP: $PUBLIC_IP"
    echo ""

    if [ "$SERVER_STATUS" != "on" ]; then
        echo "ERROR: Server exists but is not running (status: $SERVER_STATUS)" >&2
        echo "Please wait for it to be ready or delete it first." >&2
        exit 1
    fi

    # Check if we can reach it via Tailscale SSH
    echo "Checking Tailscale SSH connectivity to $HOSTNAME..."
    if ssh $SSH_OPTS "ubuntu@$HOSTNAME" "echo 'Tailscale SSH ready'" 2>/dev/null; then
        read -p "Re-provision this server via Tailscale SSH? [y/N] " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            echo "Aborted."
            exit 0
        fi

        echo ""
        echo "Re-provisioning server via Tailscale SSH..."
        provision_server "$HOSTNAME"

        echo ""
        echo "=========================================="
        echo "Server re-provisioned!"
        echo "=========================================="
        echo "  Hostname:  $HOSTNAME"
        echo "  Server ID: $SERVER_ID"
        echo "  SSH:       ssh ubuntu@$HOSTNAME"
        echo "=========================================="
        exit 0
    else
        echo "Cannot reach server via Tailscale SSH"
        echo "Will attempt to set up Tailscale via direct SSH..."

        # Wait for direct SSH to be available
        echo "Waiting for direct SSH to $PUBLIC_IP..."
        START_TIME=$(date +%s)
        while true; do
            ELAPSED=$(($(date +%s) - START_TIME))
            if [ $ELAPSED -ge 300 ]; then
                echo "ERROR: Timed out waiting for SSH" >&2
                exit 1
            fi
            if ssh $DIRECT_SSH_OPTS "ubuntu@$PUBLIC_IP" "echo 'SSH ready'" 2>/dev/null; then
                echo "  Direct SSH connected!"
                break
            fi
            echo "  Waiting... (${ELAPSED}s elapsed)"
            sleep 10
        done

        # Setup Tailscale
        echo ""
        echo "=== Setting up Tailscale ==="
        setup_tailscale "ubuntu@$PUBLIC_IP" "$HOSTNAME"

        # Wait for Tailscale SSH
        echo ""
        echo "Waiting for Tailscale SSH to be accessible..."
        START_TIME=$(date +%s)
        while true; do
            ELAPSED=$(($(date +%s) - START_TIME))
            if [ $ELAPSED -ge 120 ]; then
                echo "ERROR: Timed out waiting for Tailscale SSH" >&2
                exit 1
            fi
            if ssh $SSH_OPTS "ubuntu@$HOSTNAME" "echo 'Tailscale SSH ready'" 2>/dev/null; then
                echo "  Tailscale SSH connected!"
                break
            fi
            echo "  Waiting... (${ELAPSED}s elapsed)"
            sleep 10
        done

        read -p "Provision this server? [y/N] " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            echo "Aborted."
            exit 0
        fi

        provision_server "$HOSTNAME"

        echo ""
        echo "=========================================="
        echo "Server provisioned!"
        echo "=========================================="
        echo "  Hostname:  $HOSTNAME"
        echo "  Server ID: $SERVER_ID"
        echo "  SSH:       ssh ubuntu@$HOSTNAME"
        echo "=========================================="
        exit 0
    fi
fi

# Create new server
echo "No existing server found. Will create:"
echo "  Hostname: $HOSTNAME"
echo "  Plan:     $PLAN"
echo "  Site:     $SITE"
echo "  OS:       $OS"
echo "  Project:  $PROJECT"
echo ""

read -p "Create this server? [y/N] " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 0
fi

echo ""
echo "Creating server..."
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "https://api.latitude.sh/servers" \
    -H "Authorization: Bearer $LATITUDE_API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
        \"data\": {
            \"type\": \"servers\",
            \"attributes\": {
                \"project\": \"$PROJECT\",
                \"plan\": \"$PLAN\",
                \"site\": \"$SITE\",
                \"operating_system\": \"$OS\",
                \"hostname\": \"$HOSTNAME\",
                \"billing\": \"hourly\",
                \"ssh_keys\": [\"ssh_vAPXaMkXpNepz\"]
            }
        }
    }")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -ne 201 ]; then
    echo "ERROR: Failed to create server (HTTP $HTTP_CODE)" >&2
    echo "$BODY" | jq . 2>/dev/null || echo "$BODY"
    exit 1
fi

SERVER_ID=$(echo "$BODY" | jq -r '.data.id')
if [ -z "$SERVER_ID" ] || [ "$SERVER_ID" = "null" ]; then
    echo "ERROR: Failed to extract server ID" >&2
    echo "$BODY"
    exit 1
fi

echo "Server created: $SERVER_ID"

# Wait for server to be running
echo "Waiting for server to be ready..."
START_TIME=$(date +%s)
PUBLIC_IP=""

while true; do
    ELAPSED=$(($(date +%s) - START_TIME))
    if [ $ELAPSED -ge $SSH_TIMEOUT ]; then
        echo "ERROR: Timed out waiting for server to be ready" >&2
        exit 1
    fi

    STATUS_RESPONSE=$(curl -s "https://api.latitude.sh/servers/$SERVER_ID" \
        -H "Authorization: Bearer $LATITUDE_API_KEY")

    STATUS=$(echo "$STATUS_RESPONSE" | jq -r '.data.attributes.status')

    if [ "$STATUS" = "failed_deployment" ] || [ "$STATUS" = "failed_disk_erasing" ]; then
        echo "ERROR: Server deployment failed with status: $STATUS" >&2
        exit 1
    fi

    if [ "$STATUS" = "on" ]; then
        PUBLIC_IP=$(echo "$STATUS_RESPONSE" | jq -r '.data.attributes.primary_ipv4')
        if [ -n "$PUBLIC_IP" ] && [ "$PUBLIC_IP" != "null" ]; then
            echo "  Status: $STATUS (IP: $PUBLIC_IP)"
            break
        fi
    fi

    echo "  Status: $STATUS (${ELAPSED}s elapsed)"
    sleep 10
done

# Wait for SSH to be available
echo "Waiting for SSH..."
while true; do
    ELAPSED=$(($(date +%s) - START_TIME))
    if [ $ELAPSED -ge $SSH_TIMEOUT ]; then
        echo "ERROR: Timed out waiting for SSH" >&2
        exit 1
    fi

    if ssh $DIRECT_SSH_OPTS "ubuntu@$PUBLIC_IP" "echo 'SSH ready'" 2>/dev/null; then
        echo "  SSH connected! (${ELAPSED}s elapsed)"
        break
    fi

    echo "  Waiting for SSH... (${ELAPSED}s elapsed)"
    sleep 10
done

# Setup Tailscale via direct SSH
echo ""
echo "=== Setting up Tailscale ==="
setup_tailscale "ubuntu@$PUBLIC_IP" "$HOSTNAME"

# Wait for Tailscale SSH to be available
echo ""
echo "Waiting for Tailscale SSH to be accessible..."
START_TIME=$(date +%s)
while true; do
    ELAPSED=$(($(date +%s) - START_TIME))
    if [ $ELAPSED -ge 120 ]; then
        echo "ERROR: Timed out waiting for Tailscale SSH" >&2
        echo "Direct SSH still available at: ssh ubuntu@$PUBLIC_IP" >&2
        exit 1
    fi

    if ssh $SSH_OPTS "ubuntu@$HOSTNAME" "echo 'Tailscale SSH ready'" 2>/dev/null; then
        echo "  Tailscale SSH connected! (${ELAPSED}s elapsed)"
        break
    fi

    echo "  Waiting for Tailscale... (${ELAPSED}s elapsed)"
    sleep 10
done

# Provision the server via Tailscale SSH
provision_server "$HOSTNAME"

echo ""
echo "=========================================="
echo "Server setup complete!"
echo "=========================================="
echo "  Hostname:  $HOSTNAME"
echo "  Server ID: $SERVER_ID"
echo "  Public IP: $PUBLIC_IP"
echo "  SSH:       ssh ubuntu@$HOSTNAME"
echo "=========================================="
