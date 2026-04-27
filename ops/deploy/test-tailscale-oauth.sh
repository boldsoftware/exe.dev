#!/bin/bash
#
# This script makes sure your Tailscale Oauth client keys are
# in the right state to setup bold server VMs.
#

set -euo pipefail

preflight_tailscale_oauth() {
    : "${TS_OAUTH_CLIENT_ID:?set TS_OAUTH_CLIENT_ID}"
    : "${TS_OAUTH_CLIENT_SECRET:?set TS_OAUTH_CLIENT_SECRET}"
    : "${TS_TAG:=tag:exelet}"

    # TS_TAG may be a single tag or a comma-separated list. Build a JSON
    # array for the API call and a human-readable label for messages.
    local IFS_BAK="$IFS"
    IFS=',' read -r -a _tag_arr <<<"$TS_TAG"
    IFS="$IFS_BAK"
    local _tag_json="" _tag
    for _tag in "${_tag_arr[@]}"; do
        _tag="${_tag## }"
        _tag="${_tag%% }"
        [ -z "$_tag" ] && continue
        if [ -z "$_tag_json" ]; then
            _tag_json="\"${_tag}\""
        else
            _tag_json="${_tag_json}, \"${_tag}\""
        fi
    done
    local TS_TAG_LABEL="${TS_TAG}"

    # Prefer explicit tailnet if you run multiple orgs; otherwise use "-"
    local slug="${TS_TAILNET_SLUG:--}"
    local BASE="https://api.tailscale.com/api/v2/tailnet/${slug%/}/"

    echo "== Tailscale OAuth Client Preflight Check"
    echo "Target tag(s): ${TS_TAG_LABEL}"
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
        echo "     • Allowed tag(s): ${TS_TAG_LABEL}"
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

    echo "→ Step 2: Testing key creation for ${TS_TAG_LABEL}..."

    # Try creating a key with the requested tag set
    local resp http body key_id error_msg
    resp=$(_post "${BASE}keys" "$(
        cat <<JSON
{
  "capabilities": {"devices": {"create": {
    "reusable": false,
    "ephemeral": false,
    "tags": [${_tag_json}]
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
        echo "✓ Can create keys with ${TS_TAG_LABEL}"
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

    echo "✗ FAILED to create key with ${TS_TAG_LABEL} (HTTP $http)"
    echo "  Error: ${error_msg:-$body}"
    echo

    echo "→ Step 3: Testing each requested tag individually..."
    echo "  (this isolates which specific tag the OAuth client / tailnet ACL is rejecting)"
    echo

    # Test each requested tag in isolation so we can pinpoint the bad one.
    local good_tags=() bad_tags=() bad_errors=() per_tag t per_resp per_http per_body per_err
    for t in "${_tag_arr[@]}"; do
        t="${t## }"
        t="${t%% }"
        [ -z "$t" ] && continue

        per_resp=$(_post "${BASE}keys" "$(
            cat <<JSON
{
  "capabilities": {"devices": {"create": {
    "reusable": false,
    "ephemeral": true,
    "tags": ["${t}"]
  }}},
  "expirySeconds": 60
}
JSON
        )" 2>/dev/null)
        per_http=$(printf "%s\n" "$per_resp" | tail -n1)
        per_body=$(printf "%s\n" "$per_resp" | sed '$d')

        if [ "$per_http" = "200" ]; then
            key_id=$(printf "%s" "$per_body" | _json_get_id || true)
            _delete_key "$key_id"
            good_tags+=("$t")
            echo "  ✓ ${t} — accepted"
        else
            per_err=$(printf "%s" "$per_body" | /usr/bin/python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    print(data.get('message', ''))
except:
    pass
" 2>/dev/null || echo "")
            bad_tags+=("$t")
            bad_errors+=("${per_err:-HTTP $per_http}")
            echo "  ✗ ${t} — rejected (${per_err:-HTTP $per_http})"
        fi
    done

    echo
    echo "DIAGNOSIS:"
    if [ ${#bad_tags[@]} -eq 0 ]; then
        echo "  All individual tags work, but the combined set ${TS_TAG_LABEL} does not."
        echo "  This usually means the tailnet ACL's tagOwners doesn't permit"
        echo "  these tags to be applied together. Check the policy file's"
        echo "  tagOwners section — e.g. tag:exelet's owners may need to include"
        echo "  the OAuth client (autogroup:owner) or one of the other tags in the set."
        echo
        echo "  Reference: https://tailscale.com/kb/1068/acl-tags#tagowners"
    else
        echo "  The OAuth client / tailnet rejects these specific tag(s):"
        local i
        for i in "${!bad_tags[@]}"; do
            echo "    • ${bad_tags[$i]}  —  ${bad_errors[$i]}"
        done
        echo
        echo "  Tags that did work in isolation:"
        if [ ${#good_tags[@]} -gt 0 ]; then
            for t in "${good_tags[@]}"; do
                echo "    • ${t}"
            done
        else
            echo "    (none)"
        fi
        echo
        echo "TO FIX:"
        echo "  1. Confirm each rejected tag is listed under the OAuth client's"
        echo "     auth_keys:write scope at:"
        echo "       https://login.tailscale.com/admin/settings/oauth"
        echo "     (auth_keys with no :write suffix is read-only and won't work)"
        echo "  2. Confirm each rejected tag has a tagOwners entry in the tailnet"
        echo "     policy file at:"
        echo "       https://login.tailscale.com/admin/acls/file"
        echo "     A tag must be defined in tagOwners or the API will reject it"
        echo "     even if the OAuth client lists it as allowed."
    fi
    exit 1
}

preflight_tailscale_oauth
