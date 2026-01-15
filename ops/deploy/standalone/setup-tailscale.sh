#!/bin/bash
set -euo pipefail

HOSTNAME="$(hostname -s)"
echo "INFO: configuring Tailscale for $HOSTNAME"

if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: this script must be run as root" >&2
    exit 1
fi

if [ -z "${TS_OAUTH_CLIENT_ID:-}" ] || [ -z "${TS_OAUTH_CLIENT_SECRET:-}" ]; then
    echo "ERROR: Tailscale OAuth credentials not set" >&2
    echo "Required environment variables:" >&2
    echo "  TS_OAUTH_CLIENT_ID" >&2
    echo "  TS_OAUTH_CLIENT_SECRET" >&2
    exit 1
fi

echo "=== Installing dependencies ==="
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y curl jq

echo "=== Installing Tailscale ==="
curl -fsSL https://tailscale.com/install.sh | sh

echo "=== Generating Tailscale auth key via OAuth ==="
OAUTH_RESPONSE=$(
    curl -s -w "\n%{http_code}" -X POST \
        "https://api.tailscale.com/api/v2/oauth/token" \
        -d "client_id=${TS_OAUTH_CLIENT_ID}" \
        -d "client_secret=${TS_OAUTH_CLIENT_SECRET}" \
        -d "grant_type=client_credentials"
)

OAUTH_HTTP=$(echo "$OAUTH_RESPONSE" | tail -n 1)
OAUTH_BODY=$(echo "$OAUTH_RESPONSE" | head -n -1)

if [ "$OAUTH_HTTP" != "200" ]; then
    echo "ERROR: Failed to get OAuth token. HTTP code: $OAUTH_HTTP" >&2
    echo "Response body: $OAUTH_BODY" >&2
    exit 1
fi

ACCESS_TOKEN=$(echo "$OAUTH_BODY" | jq -r '.access_token')
if [ -z "$ACCESS_TOKEN" ] || [ "$ACCESS_TOKEN" = "null" ]; then
    echo "ERROR: Failed to extract access token" >&2
    echo "Response body: $OAUTH_BODY" >&2
    exit 1
fi

KEY_RESPONSE=$(
    curl -s -w "\n%{http_code}" -X POST \
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
}'
)

KEY_HTTP=$(echo "$KEY_RESPONSE" | tail -n 1)
KEY_BODY=$(echo "$KEY_RESPONSE" | head -n -1)

if [ "$KEY_HTTP" != "200" ]; then
    echo "ERROR: Failed to create auth key. HTTP code: $KEY_HTTP" >&2
    echo "Response body: $KEY_BODY" >&2
    exit 1
fi

AUTH_KEY=$(echo "$KEY_BODY" | jq -r '.key')
if [ -z "$AUTH_KEY" ] || [ "$AUTH_KEY" = "null" ]; then
    echo "ERROR: Failed to extract auth key from response" >&2
    echo "Response body: $KEY_BODY" >&2
    exit 1
fi

echo "=== Connecting to Tailscale ==="
tailscale up --authkey="$AUTH_KEY" --advertise-tags=tag:server --ssh --hostname="$HOSTNAME"
tailscale status
