#!/bin/bash
# Create an exeprox host on Latitude.sh and provision it.
# This script creates the server via the Latitude API, waits for it to come
# online, then delegates to provision-exeprox-host.sh for configuration.

set -euo pipefail

LATITUDE_API="https://api.latitude.sh"

# Helper function for Latitude API calls
latitude_api() {
    local method="$1"
    local endpoint="$2"
    local data="${3:-}"

    # URL-encode brackets in the endpoint for query parameters
    local encoded_endpoint
    encoded_endpoint=$(echo "$endpoint" | sed 's/\[/%5B/g; s/\]/%5D/g')

    if [ -n "$data" ]; then
        curl -s -X "$method" \
            "${LATITUDE_API}${encoded_endpoint}" \
            -H "Authorization: Bearer ${LATITUDE_PROD_API_KEY}" \
            -H "Content-Type: application/json" \
            -d "$data"
    else
        curl -s -X "$method" \
            "${LATITUDE_API}${encoded_endpoint}" \
            -H "Authorization: Bearer ${LATITUDE_PROD_API_KEY}" \
            -H "Content-Type: application/json"
    fi
}

# Check for required parameters
if [ $# -ne 2 ]; then
    echo "Usage: $0 <latitude-region> <prod|staging>"
    echo ""
    echo "Creates an exeprox host on Latitude in the specified region."
    echo ""
    echo "Arguments:"
    echo "  latitude-region  The Latitude site code (see list below)"
    echo "  prod|staging     Environment: 'prod' for exe-prod project, 'staging' for exe-staging"
    echo ""
    echo "Required environment variables:"
    echo "  LATITUDE_PROD_API_KEY  Latitude API key"
    echo "  TS_OAUTH_CLIENT_ID     Tailscale OAuth client ID"
    echo "  TS_OAUTH_CLIENT_SECRET Tailscale OAuth client secret"
    echo "  HOST_PRIVATE_KEY       SSH host private key"
    echo "  HOST_CERT_SIG          SSH host certificate signature"
    echo ""
    echo "To get HOST_PRIVATE_KEY and HOST_CERT_SIG, run:"
    echo "  ssh exed-02 sqlite3 /home/ubuntu/exe.db \"SELECT private_key FROM ssh_host_key WHERE id = 1;\""
    echo "  ssh exed-02 sqlite3 /home/ubuntu/exe.db \"SELECT cert_sig FROM ssh_host_key WHERE id = 1;\""
    echo ""

    # List available regions if API key is set
    if [ -n "${LATITUDE_PROD_API_KEY:-}" ]; then
        echo "Available regions:"
        latitude_api GET "/regions?page[size]=100" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    regions = data.get('data', [])
    for region in sorted(regions, key=lambda r: r.get('attributes', {}).get('slug', '')):
        attrs = region.get('attributes', {})
        slug = attrs.get('slug', '')
        # city might be a string or nested, try both
        city = attrs.get('city', {})
        if isinstance(city, dict):
            city = city.get('name', '')
        # country is a dict with name
        country = attrs.get('country', {})
        if isinstance(country, dict):
            country = country.get('name', '')
        # Use name field as fallback for city
        if not city:
            city = attrs.get('name', slug)
        print(f'  {slug:6} - {city}, {country}')
except Exception as e:
    print(f'  (unable to list regions: {e})')
"
    else
        echo "Set LATITUDE_PROD_API_KEY to see available regions."
    fi
    exit 1
fi

REGION="$1"
STAGE="$2"

# Validate stage
if [ "$STAGE" != "prod" ] && [ "$STAGE" != "staging" ]; then
    echo "Error: Stage must be 'prod' or 'staging', got: $STAGE"
    exit 1
fi

# Set project based on stage
if [ "$STAGE" = "prod" ]; then
    PROJECT="exe-prod"
else
    PROJECT="exe-staging"
fi

# Check for required environment variables
if [ -z "${LATITUDE_PROD_API_KEY:-}" ]; then
    echo "ERROR: LATITUDE_PROD_API_KEY not set"
    echo "Please set the environment variable:"
    echo "  export LATITUDE_PROD_API_KEY=<your-api-key>"
    exit 1
fi

if [ -z "${TS_OAUTH_CLIENT_ID:-}" ] || [ -z "${TS_OAUTH_CLIENT_SECRET:-}" ]; then
    echo "ERROR: Tailscale OAuth credentials not set"
    echo "Please set the following environment variables:"
    echo "  export TS_OAUTH_CLIENT_ID=<your-client-id>"
    echo "  export TS_OAUTH_CLIENT_SECRET=<your-client-secret>"
    echo ""
    echo "You can get these credentials from the Tailscale admin console:"
    echo "  https://login.tailscale.com/admin/settings/oauth"
    exit 1
fi

if [[ -z "${HOST_PRIVATE_KEY:-}" ]] || [[ -z "${HOST_CERT_SIG:-}" ]]; then
    echo "ERROR: HOST_PRIVATE_KEY and/or HOST_CERT_SIG not set"
    echo ""
    echo 'export HOST_PRIVATE_KEY="$(ssh exed-02 '\''sqlite3 /home/ubuntu/exe.db "SELECT private_key FROM ssh_host_key WHERE id = 1"'\'')"'
    echo 'export HOST_CERT_SIG="$(ssh exed-02 '\''sqlite3 /home/ubuntu/exe.db "SELECT cert_sig FROM ssh_host_key WHERE id = 1"'\'')"'
    exit 1
fi

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PROVISION_SCRIPT="$SCRIPT_DIR/provision-exeprox-host.sh"
if [ ! -f "$PROVISION_SCRIPT" ]; then
    echo "ERROR: Missing $PROVISION_SCRIPT" >&2
    exit 1
fi

echo "=========================================="
echo "Setting up exeprox host on Latitude"
echo "=========================================="
echo "Region: ${REGION}"
echo "Project: ${PROJECT}"
echo ""

# Look up the project ID from the slug
echo "Looking up project ID for ${PROJECT}..."
PROJECTS_RESPONSE=$(latitude_api GET "/projects")

PROJECT_ID=$(echo "$PROJECTS_RESPONSE" | PROJECT_SLUG="$PROJECT" python3 -c '
import sys, json, os
project_slug = os.environ["PROJECT_SLUG"]
try:
    data = json.load(sys.stdin)
    if "errors" in data:
        for e in data["errors"]:
            print("ERROR: " + e.get("detail", e.get("title", str(e))), file=sys.stderr)
        sys.exit(1)
    for project in data.get("data", []):
        attrs = project.get("attributes", {})
        if attrs.get("slug") == project_slug or attrs.get("name") == project_slug:
            print(project.get("id", ""))
            sys.exit(0)
    print(f"ERROR: Project {project_slug} not found", file=sys.stderr)
    print("Available projects:", file=sys.stderr)
    for project in data.get("data", []):
        attrs = project.get("attributes", {})
        slug = attrs.get("slug", "N/A")
        name = attrs.get("name", "N/A")
        print(f"  - {slug}: {name}", file=sys.stderr)
    sys.exit(1)
except json.JSONDecodeError as e:
    print(f"ERROR: Invalid JSON response: {e}", file=sys.stderr)
    sys.exit(1)
')

if [ -z "$PROJECT_ID" ]; then
    echo "ERROR: Failed to get project ID"
    exit 1
fi

echo "Project ID: ${PROJECT_ID}"

# Generate hostname based on region and stage
# Format: exeprox-{region}-{stage}-{number}
# We'll find the next available number
echo "Determining hostname..."

# List existing servers in the project to find the next available number
EXISTING_SERVERS=$(latitude_api GET "/servers?filter[project]=${PROJECT_ID}")

# Check for API errors
echo "$EXISTING_SERVERS" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    if 'errors' in data:
        for e in data['errors']:
            print('ERROR: ' + e.get('detail', e.get('title', str(e))), file=sys.stderr)
        sys.exit(1)
except json.JSONDecodeError as e:
    print(f'ERROR: Invalid JSON response: {e}', file=sys.stderr)
    sys.exit(1)
" || exit 1

# Find the highest number for this region/stage combination
REGION_LOWER=$(echo "$REGION" | tr '[:upper:]' '[:lower:]')
PREFIX="exeprox-${REGION_LOWER}-${STAGE}"
HIGHEST_NUM=0

# Parse existing server names to find the highest number
for name in $(echo "$EXISTING_SERVERS" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for server in data.get('data', []):
        hostname = server.get('attributes', {}).get('hostname', '')
        if hostname:
            print(hostname)
except Exception as e:
    print(f'ERROR: {e}', file=sys.stderr)
    sys.exit(1)
"); do
    if [[ "$name" =~ ^${PREFIX}-([0-9]+)$ ]]; then
        num="${BASH_REMATCH[1]}"
        if [ "$num" -gt "$HIGHEST_NUM" ]; then
            HIGHEST_NUM="$num"
        fi
    fi
done

NEXT_NUM=$((HIGHEST_NUM + 1))
MACHINE_NAME="${PREFIX}-$(printf '%02d' $NEXT_NUM)"
echo "Hostname: ${MACHINE_NAME}"

# Check if hostname already exists in Tailscale
echo "Checking Tailscale for existing device..."
TS_OAUTH_RESPONSE=$(curl -s -X POST \
    "https://api.tailscale.com/api/v2/oauth/token" \
    -d "client_id=${TS_OAUTH_CLIENT_ID}" \
    -d "client_secret=${TS_OAUTH_CLIENT_SECRET}" \
    -d "grant_type=client_credentials")

TS_ACCESS_TOKEN=$(echo "$TS_OAUTH_RESPONSE" | python3 -c '
import sys, json
try:
    data = json.load(sys.stdin)
    print(data.get("access_token", ""))
except:
    pass
')

if [ -n "$TS_ACCESS_TOKEN" ]; then
    TS_DEVICES=$(curl -s -X GET \
        "https://api.tailscale.com/api/v2/tailnet/-/devices" \
        -H "Authorization: Bearer $TS_ACCESS_TOKEN")

    TS_EXISTS=$(echo "$TS_DEVICES" | python3 -c '
import sys, json
try:
    data = json.load(sys.stdin)
    target = "'"$MACHINE_NAME"'"
    for device in data.get("devices", []):
        hostname = device.get("hostname", "")
        name = device.get("name", "").split(".")[0]  # name is like "hostname.tailnet.ts.net"
        if hostname == target or name == target:
            print("exists")
            sys.exit(0)
except:
    pass
')

    if [ "$TS_EXISTS" = "exists" ]; then
        echo "ERROR: Hostname ${MACHINE_NAME} already exists in Tailscale"
        echo ""
        echo "Delete the device from Tailscale admin console before retrying:"
        echo "  https://login.tailscale.com/admin/machines"
        exit 1
    fi
    echo "Hostname available in Tailscale"
fi
echo ""

# Check plan availability - try m4.metal.small first (cheaper), then f4.metal.small
echo "Checking plan availability in ${REGION}..."

SELECTED_PLAN=""
for plan in "m4-metal-small" "f4-metal-small"; do
    echo "  Checking ${plan}..."
    PLAN_INFO=$(latitude_api GET "/plans?filter[slug]=${plan}")

    # Check if this plan is available in the specified region
    AVAILABLE=$(echo "$PLAN_INFO" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for plan in data.get('data', []):
        regions = plan.get('attributes', {}).get('regions', [])
        for region in regions:
            locations = region.get('locations', {})
            in_stock = locations.get('in_stock', [])
            if '${REGION}' in in_stock:
                print('yes')
                sys.exit(0)
    print('no')
except Exception as e:
    print('no')
" 2>/dev/null)

    if [ "$AVAILABLE" = "yes" ]; then
        SELECTED_PLAN="$plan"
        echo "  Found available plan: ${SELECTED_PLAN}"
        break
    else
        echo "  ${plan} not available in ${REGION}"
    fi
done

if [ -z "$SELECTED_PLAN" ]; then
    echo ""
    echo "ERROR: No suitable plan (m4-metal-small or f4-metal-small) available in ${REGION}"
    echo ""
    echo "Available plans in ${REGION}:"
    latitude_api GET "/plans?filter[location]=${REGION}&filter[in_stock]=true" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for plan in data.get('data', []):
        slug = plan.get('attributes', {}).get('slug', '')
        name = plan.get('attributes', {}).get('name', '')
        print(f'  - {slug}: {name}')
except:
    print('  (unable to list plans)')
"
    exit 1
fi

echo ""

# Fetch SSH keys from the project
echo "Fetching SSH keys..."
SSH_KEYS_RESPONSE=$(latitude_api GET "/projects/${PROJECT_ID}/ssh_keys")

SSH_KEY_IDS=$(echo "$SSH_KEYS_RESPONSE" | python3 -c '
import sys, json
try:
    data = json.load(sys.stdin)
    if "errors" in data:
        for e in data["errors"]:
            print("ERROR: " + e.get("detail", e.get("title", str(e))), file=sys.stderr)
        sys.exit(1)
    keys = data.get("data", [])
    if not keys:
        print("ERROR: No SSH keys found in project", file=sys.stderr)
        sys.exit(1)
    # Output comma-separated list of key IDs
    key_ids = [k.get("id", "") for k in keys if k.get("id")]
    print(",".join(key_ids))
except json.JSONDecodeError as e:
    print(f"ERROR: Invalid JSON response: {e}", file=sys.stderr)
    sys.exit(1)
')

if [ -z "$SSH_KEY_IDS" ]; then
    echo "ERROR: Failed to get SSH key IDs"
    exit 1
fi

echo "SSH Key IDs: ${SSH_KEY_IDS}"

# Build SSH keys JSON array
SSH_KEYS_JSON=$(echo "$SSH_KEY_IDS" | tr ',' '\n' | python3 -c '
import sys, json
keys = [line.strip() for line in sys.stdin if line.strip()]
print(json.dumps(keys))
')

echo "Creating server..."
echo "  Plan: ${SELECTED_PLAN}"
echo "  Region: ${REGION}"
echo "  Hostname: ${MACHINE_NAME}"
echo "  SSH Keys: ${SSH_KEY_IDS}"
echo ""

# Create the server
CREATE_RESPONSE=$(latitude_api POST "/servers" "$(
    cat <<EOF
{
  "data": {
    "type": "servers",
    "attributes": {
      "project": "${PROJECT_ID}",
      "plan": "${SELECTED_PLAN}",
      "site": "${REGION}",
      "operating_system": "ubuntu_24_04_x64_lts",
      "hostname": "${MACHINE_NAME}",
      "billing": "hourly",
      "ssh_keys": ${SSH_KEYS_JSON}
    }
  }
}
EOF
)")

# Check for errors
ERROR_MSG=$(echo "$CREATE_RESPONSE" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    errors = data.get('errors', [])
    if errors:
        for e in errors:
            print(e.get('detail', e.get('title', str(e))))
        sys.exit(0)
except:
    pass
" 2>/dev/null)

if [ -n "$ERROR_MSG" ]; then
    echo "ERROR: Failed to create server"
    echo "$ERROR_MSG"
    exit 1
fi

# Extract server ID and details
SERVER_ID=$(echo "$CREATE_RESPONSE" | python3 -c "
import sys, json
data = json.load(sys.stdin)
print(data.get('data', {}).get('id', ''))
")

if [ -z "$SERVER_ID" ]; then
    echo "ERROR: Failed to get server ID from response"
    echo "$CREATE_RESPONSE"
    exit 1
fi

echo "Server created with ID: ${SERVER_ID}"
echo ""

# Wait for server to be ready
echo "Waiting for server to be provisioned..."
echo "(This can take several minutes for bare metal servers)"
echo ""

MAX_WAIT=1800 # 30 minutes
WAIT_INTERVAL=30
ELAPSED=0
SERVER_IP=""

while [ $ELAPSED -lt $MAX_WAIT ]; do
    SERVER_INFO=$(latitude_api GET "/servers/${SERVER_ID}")

    STATUS=$(echo "$SERVER_INFO" | python3 -c "
import sys, json
data = json.load(sys.stdin)
print(data.get('data', {}).get('attributes', {}).get('status', ''))
")

    if [ "$STATUS" = "on" ]; then
        # Get the primary IP address
        SERVER_IP=$(echo "$SERVER_INFO" | python3 -c "
import sys, json
data = json.load(sys.stdin)
ips = data.get('data', {}).get('attributes', {}).get('primary_ipv4', '')
print(ips)
")

        if [ -n "$SERVER_IP" ]; then
            echo "Server is ready!"
            echo "  Status: ${STATUS}"
            echo "  IP: ${SERVER_IP}"
            break
        fi
    fi

    echo "  Status: ${STATUS} - waiting... ($ELAPSED/$MAX_WAIT seconds)"
    sleep $WAIT_INTERVAL
    ELAPSED=$((ELAPSED + WAIT_INTERVAL))
done

if [ -z "$SERVER_IP" ]; then
    echo "ERROR: Server did not become ready within ${MAX_WAIT} seconds"
    echo "Check the Latitude dashboard for server status"
    exit 1
fi

# Provision the server (installs Tailscale, builds and deploys sshpiper, etc.)
"$PROVISION_SCRIPT" "$MACHINE_NAME" "$SERVER_IP"

echo ""
echo "=========================================="
echo "Setup complete!"
echo "=========================================="
echo ""
echo "Server details:"
echo "  Hostname: ${MACHINE_NAME}"
echo "  Server ID: ${SERVER_ID}"
echo "  Public IP: ${SERVER_IP}"
echo "  Plan: ${SELECTED_PLAN}"
echo "  Region: ${REGION}"
echo "  Project: ${PROJECT}"
echo ""
echo "Connect via Tailscale:"
echo "  ssh ubuntu@${MACHINE_NAME}"
echo ""
echo "Connect via public IP:"
echo "  ssh ubuntu@${SERVER_IP}"
echo "=========================================="
