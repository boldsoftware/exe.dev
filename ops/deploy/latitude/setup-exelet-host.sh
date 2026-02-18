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
  TS_OAUTH_CLIENT_ID        Tailscale OAuth client ID (passed to provision script)
  TS_OAUTH_CLIENT_SECRET    Tailscale OAuth client secret (passed to provision script)

Optional environment variables:
  LATITUDE_SITE             Latitude.sh site (default: LAX2)
  LATITUDE_PLAN             Server plan (default: rs4-metal-xlarge)
  SSH_KEY                   Path to SSH private key for direct IP access
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

# Configuration
PROJECT="$LATITUDE_PROJECT"
SITE="${LATITUDE_SITE:-LAX2}"
PLAN="${LATITUDE_PLAN:-rs4-metal-xlarge}"
# Latitude website shows plans with dots, but API requires hyphens
PLAN="${PLAN//./-}"
OS="ubuntu_24_04_x64_lts"
SSH_TIMEOUT=1800 # 30 minutes

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROVISION_SCRIPT="$SCRIPT_DIR/provision-exelet-host.sh"

if [ ! -f "$PROVISION_SCRIPT" ]; then
    echo "ERROR: Missing $PROVISION_SCRIPT" >&2
    exit 1
fi

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

    read -p "Provision this server? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborted."
        exit 0
    fi

    "$PROVISION_SCRIPT" "$HOSTNAME" "$PUBLIC_IP"
    exit 0
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

# Provision the server (installs Tailscale, sets up drives, etc.)
"$PROVISION_SCRIPT" "$HOSTNAME" "$PUBLIC_IP"

echo ""
echo "=========================================="
echo "Server setup complete!"
echo "=========================================="
echo "  Hostname:  $HOSTNAME"
echo "  Server ID: $SERVER_ID"
echo "  Public IP: $PUBLIC_IP"
echo "  SSH:       ssh ubuntu@$HOSTNAME"
echo "=========================================="
