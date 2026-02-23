#!/usr/bin/env bash
set -e
trap 'echo Error in $0 at line $LINENO' ERR

# Honeycomb MCP OAuth2 authentication.
# Registers an OAuth client, walks you through the auth flow,
# and saves the token to ~/.config/mcp/mcp_servers.json for mcp-cli.
#
# Usage: ./ops/honeycomb-mcp-auth.sh
#
# On headless VMs (like exe.dev), the localhost redirect won't reach you.
# The script will ask you to paste the redirect URL after authorizing.

TOKEN_FILE="${HOME}/.config/mcp/honeycomb_token.json"
CONFIG_FILE="${HOME}/.config/mcp/mcp_servers.json"
CALLBACK_URL="http://localhost:9876/callback"

echo "==> Registering OAuth client with Honeycomb..."
REG_RESPONSE=$(curl -sf https://ui.honeycomb.io/oauth/register \
    -H 'Content-Type: application/json' \
    -d "{
    \"client_name\": \"mcp-cli-exedev\",
    \"redirect_uris\": [\"${CALLBACK_URL}\"],
    \"grant_types\": [\"authorization_code\", \"refresh_token\"],
    \"response_types\": [\"code\"],
    \"token_endpoint_auth_method\": \"none\",
    \"scope\": \"mcp:read mcp:write\"
  }")

CLIENT_ID=$(echo "$REG_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['client_id'])")
echo "    Client ID: ${CLIENT_ID}"

# PKCE
CODE_VERIFIER=$(python3 -c "import secrets; print(secrets.token_urlsafe(96)[:128])")
CODE_CHALLENGE=$(printf '%s' "$CODE_VERIFIER" | openssl dgst -sha256 -binary | base64 | tr '+/' '-_' | tr -d '=')
STATE=$(python3 -c "import secrets; print(secrets.token_hex(16))")

ENCODED_REDIRECT=$(python3 -c "import urllib.parse; print(urllib.parse.quote('${CALLBACK_URL}'))")
AUTH_URL="https://ui.honeycomb.io/oauth/authorize?response_type=code&client_id=${CLIENT_ID}&redirect_uri=${ENCODED_REDIRECT}&scope=mcp%3Aread+mcp%3Awrite&state=${STATE}&code_challenge=${CODE_CHALLENGE}&code_challenge_method=S256"

echo ""
echo "==> Open this URL in your browser:"
echo ""
echo "    ${AUTH_URL}"
echo ""
echo "==> After authorizing, your browser will redirect to localhost:9876."
echo "    Copy the FULL URL from your browser's address bar and paste it here."
echo "    (It will start with http://localhost:9876/callback?code=...)"
echo ""
read -rp "Paste redirect URL: " REDIRECT_URL

AUTH_CODE=$(python3 -c "
import urllib.parse, sys
q = urllib.parse.parse_qs(urllib.parse.urlparse(sys.argv[1]).query)
if 'code' not in q:
    print('Error: no code found in URL', file=sys.stderr)
    sys.exit(1)
print(q['code'][0])
" "$REDIRECT_URL")

echo "==> Exchanging code for token..."
TOKEN_RESPONSE=$(curl -sf https://ui.honeycomb.io/oauth/token \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d "grant_type=authorization_code&code=${AUTH_CODE}&redirect_uri=${ENCODED_REDIRECT}&client_id=${CLIENT_ID}&code_verifier=${CODE_VERIFIER}")

ACCESS_TOKEN=$(echo "$TOKEN_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")

mkdir -p "$(dirname "$TOKEN_FILE")"
echo "$TOKEN_RESPONSE" >"$TOKEN_FILE"
chmod 600 "$TOKEN_FILE"
echo "==> Token saved to ${TOKEN_FILE}"

# Update mcp_servers.json
python3 -c "
import json, os
config = {
    'mcpServers': {
        'honeycomb': {
            'url': 'https://mcp.honeycomb.io/mcp',
            'headers': {
                'Authorization': 'Bearer ${ACCESS_TOKEN}'
            }
        }
    }
}
with open('${CONFIG_FILE}', 'w') as f:
    json.dump(config, f, indent=2)
os.chmod('${CONFIG_FILE}', 0o600)
"
echo "==> Updated ${CONFIG_FILE}"

# Test
echo ""
echo "Testing connection..."
HTTP_CODE=$(curl -s -o /dev/null -w '%{http_code}' https://mcp.honeycomb.io/mcp \
    -X POST \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}')

if [ "$HTTP_CODE" = "200" ]; then
    echo "==> Success! Honeycomb MCP is ready."
    echo ""
    echo "Usage:  mcp-cli call honeycomb run_query '{...}'"
else
    echo "==> Warning: got HTTP ${HTTP_CODE} from Honeycomb MCP"
fi
