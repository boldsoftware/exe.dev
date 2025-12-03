#!/bin/bash
#
# This script makes sure your Tailscale Oauth client keys are
# in the right state to setup bold server VMs.
#

set -euo pipefail

preflight_tailscale_oauth() {
    : "${TS_OAUTH_CLIENT_ID:?set TS_OAUTH_CLIENT_ID}"
    : "${TS_OAUTH_CLIENT_SECRET:?set TS_OAUTH_CLIENT_SECRET}"
    : "${TS_TAG:=tag:server}"

    # Prefer explicit tailnet if you run multiple orgs; otherwise use "-"
    local slug="${TS_TAILNET_SLUG:--}"
    local BASE="https://api.tailscale.com/api/v2/tailnet/${slug%/}/"

    echo "== Tailscale OAuth Client Preflight Check"
    echo "Target tag: ${TS_TAG}"
    echo "Tailnet: ${slug}"
    echo

    # First, get an OAuth access token
    echo "→ Step 1: Authenticating OAuth client..."

    # Check credential format
    if [[ ! "$TS_OAUTH_CLIENT_ID" =~ ^[a-zA-Z0-9]+$ ]]; then
        echo "✗ Invalid TS_OAUTH_CLIENT_ID format"
        echo "  Current value: ${TS_OAUTH_CLIENT_ID}"
        echo "  Expected: alphanumeric string (e.g., 'k5KivtDAVq11CNTRL')"
        echo
        echo "TO FIX: Check your OAuth client ID is correctly copied"
        exit 1
    fi

    if [[ ! "$TS_OAUTH_CLIENT_SECRET" =~ ^tskey-client- ]]; then
        echo "✗ Invalid TS_OAUTH_CLIENT_SECRET format"
        echo "  Expected format: tskey-client-xxxxx-yyyyy..."
        echo "  Current prefix: $(echo "$TS_OAUTH_CLIENT_SECRET" | cut -c1-13)..."
        echo
        echo "TO FIX: OAuth client secrets always start with 'tskey-client-'"
        echo "        Make sure you copied the entire secret correctly"
        exit 1
    fi

    local oauth_resp oauth_http oauth_body access_token
    oauth_resp=$(/usr/bin/curl -s -w "\n%{http_code}" -X POST \
        "https://api.tailscale.com/api/v2/oauth/token" \
        -d "client_id=${TS_OAUTH_CLIENT_ID}" \
        -d "client_secret=${TS_OAUTH_CLIENT_SECRET}" \
        -d "grant_type=client_credentials")

    oauth_http=$(printf "%s\n" "$oauth_resp" | tail -n1)
    oauth_body=$(printf "%s\n" "$oauth_resp" | sed '$d')

    if [ "$oauth_http" = "401" ]; then
        echo "✗ Authentication failed (HTTP 401)"
        echo "The OAuth client ID and secret are not valid"
        echo
        echo "TO FIX:"
        echo "  1. Create a new Tailscale OAuth client with:"
        echo "     • Scopes required:"
        echo "       - devices:core:write (for device management)"
        echo "       - auth_keys:write (for creating auth keys)"
        echo "     • Allowed tag: ${TS_TAG}"
        exit 1
    elif [ "$oauth_http" != "200" ]; then
        echo "✗ Unexpected error (HTTP $oauth_http)"
        echo "Response: $oauth_body"
        exit 1
    fi

    # Extract access token and scopes
    local scopes
    access_token=$(printf "%s" "$oauth_body" | /usr/bin/python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    token = data.get('access_token')
    if token:
        print(token)
    else:
        sys.exit(1)
except:
    sys.exit(1)
" || true)

    scopes=$(printf "%s" "$oauth_body" | /usr/bin/python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    scope = data.get('scope', '')
    print(scope)
except:
    print('')
" || true)

    if [ -z "$access_token" ]; then
        echo "✗ FAILED: Could not extract access token"
        echo "Response: $oauth_body"
        exit 1
    fi

    echo "✓ OAuth authentication successful"

    # Parse the scopes to understand what we have
    local has_devices="no"
    local has_auth_keys="no"
    local has_all="no"

    if [ -n "$scopes" ]; then
        if [[ "$scopes" == *"devices"* ]]; then
            has_devices="yes"
        fi
        if [[ "$scopes" == *"auth_keys"* ]]; then
            has_auth_keys="yes"
        fi
        if [[ "$scopes" == *"all"* ]]; then
            has_all="yes"
            has_devices="yes"
            has_auth_keys="yes"
        fi

        echo "  OAuth client scopes: ${scopes}"

        if [ "$has_auth_keys" = "no" ] && [ "$has_all" = "no" ]; then
            echo
            echo "  ⚠️  WARNING: Missing 'auth_keys:write' scope!"
        fi

        if [ "$has_devices" = "no" ] && [ "$has_all" = "no" ]; then
            echo
            echo "  ⚠️  WARNING: Missing 'devices:core:write' scope!"
        fi
    else
        echo "  OAuth client scopes: (unable to determine)"
    fi
    echo

    # Helper: POST JSON with Bearer auth, print body + last line HTTP code
    _post() {
        /usr/bin/curl -s -w "\n%{http_code}" -X POST \
            "${1}" \
            -H "Authorization: Bearer ${access_token}" \
            -H "Content-Type: application/json" \
            -d "${2}"
    }

    # Helper: GET with Bearer auth
    _get() {
        /usr/bin/curl -s -w "\n%{http_code}" -X GET \
            "${1}" \
            -H "Authorization: Bearer ${access_token}"
    }

    # Helper: DELETE by id (best-effort)
    _delete_key() {
        local key_id="$1"
        [ -z "$key_id" ] && return 0
        /usr/bin/curl -s -X DELETE \
            "${BASE}keys/${key_id}" \
            -H "Authorization: Bearer ${access_token}" >/dev/null 2>&1 || true
    }

    # Helper: extract .id with system python3 (no jq)
    _json_get_id() {
        /usr/bin/python3 - <<'PY'
import sys, json
try:
    data=json.load(sys.stdin)
    v=data.get("id")
    if v is None: sys.exit(1)
    print(v)
except Exception:
    sys.exit(1)
PY
    }

    echo "→ Step 2: Testing key creation for ${TS_TAG}..."

    # First, try creating a key with tag:server specifically
    local resp http body key_id error_msg
    resp=$(_post "${BASE}keys" "$(
        cat <<JSON
{
  "capabilities": {"devices": {"create": {
    "reusable": false,
    "ephemeral": false,
    "tags": ["${TS_TAG}"]
  }}},
  "expirySeconds": 60
}
JSON
    )")
    http=$(printf "%s\n" "$resp" | tail -n1)
    body=$(printf "%s\n" "$resp" | sed '$d')

    if [ "$http" = "200" ]; then
        # Success! Clean up the test key
        key_id=$(printf "%s" "$body" | _json_get_id || true)
        _delete_key "$key_id"
        echo "✓ Can create keys with ${TS_TAG}"
        echo
        echo "✅ Tailscale OAuth client is properly configured!"
        return 0
    fi

    # Failed to create tagged key - let's be more diagnostic
    error_msg=$(printf "%s" "$body" | /usr/bin/python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    print(data.get('message', ''))
except:
    pass
" 2>/dev/null || echo "")

    echo "✗ FAILED to create key with ${TS_TAG} (HTTP $http)"
    echo "  Error: ${error_msg:-$body}"
    echo

    echo "→ Step 3: Determining what tags this OAuth client can use..."

    # Since we can't create untagged keys, let's try some common tags
    local test_tags=("tag:prod" "tag:dev" "tag:container" "tag:docker" "tag:server")
    local working_tag=""

    for test_tag in "${test_tags[@]}"; do
        if [ "$test_tag" = "${TS_TAG}" ]; then
            continue # Skip the one we already tried
        fi

        resp=$(_post "${BASE}keys" "$(
            cat <<JSON
{
  "capabilities": {"devices": {"create": {
    "reusable": false,
    "ephemeral": true,
    "tags": ["${test_tag}"]
  }}},
  "expirySeconds": 60
}
JSON
        )" 2>/dev/null)
        http=$(printf "%s\n" "$resp" | tail -n1)

        if [ "$http" = "200" ]; then
            working_tag="$test_tag"
            key_id=$(printf "%s\n" "$resp" | sed '$d' | _json_get_id || true)
            _delete_key "$key_id"
            echo "  ✓ Found working tag: ${test_tag}"
            break
        fi
    done

    echo
    echo "DIAGNOSIS:"
    echo "  Your OAuth client has the auth_keys scope but"
    echo "  is NOT configured to use ${TS_TAG}"

    if [ -n "$working_tag" ]; then
        echo
        echo "  This OAuth client CAN create keys with: ${working_tag}"
        echo "  But it CANNOT create keys with: ${TS_TAG}"
    fi

    echo
    echo "TO FIX:"
    echo "  1. Go to: https://login.tailscale.com/admin/settings/oauth"
    echo "  2. Find your OAuth client"
    echo "  3. Edit the client and ensure it has:"
    echo "     • Scopes:"
    echo "       - devices:core:write (for device management)"
    echo "       - auth_keys:write (for creating auth keys)"
    echo "     • In the 'auth_keys:write' scope section:"
    echo "       - Add ${TS_TAG} to the allowed tags"
    if [ -n "$working_tag" ]; then
        echo "     - Currently has: ${working_tag}"
        echo "     - Needs to have: ${TS_TAG}"
    fi
    echo "  5. Save the changes"
    echo
    echo "ALTERNATIVE:"
    if [ -n "$working_tag" ]; then
        echo "  Use the working tag instead:"
        echo "    export TS_TAG='${working_tag}'"
    else
        echo "  Create a new OAuth client with ${TS_TAG} in allowed tags"
    fi
    exit 1
}

preflight_tailscale_oauth
