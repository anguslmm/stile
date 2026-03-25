#!/usr/bin/env bash
# Get an OIDC token from the Dex provider running in docker-compose.scaling.yml.
#
# Usage:
#   scripts/get-oidc-token.sh                  # default: alice@example.com
#   scripts/get-oidc-token.sh bob@example.com  # specific user
#
# Both users have password "password".

set -euo pipefail

DEX_URL="${DEX_URL:-http://localhost:5556}"
USER="${1:-alice@example.com}"
PASSWORD="${PASSWORD:-password}"
CLIENT_ID="${CLIENT_ID:-stile}"
CLIENT_SECRET="${CLIENT_SECRET:-stile-secret}"

RESPONSE=$(curl -sf -X POST "${DEX_URL}/dex/token" \
  -d grant_type=password \
  -d "username=${USER}" \
  -d "password=${PASSWORD}" \
  -d "client_id=${CLIENT_ID}" \
  -d "client_secret=${CLIENT_SECRET}" \
  -d "scope=openid email profile")

ID_TOKEN=$(echo "$RESPONSE" | jq -r '.id_token')
ACCESS_TOKEN=$(echo "$RESPONSE" | jq -r '.access_token')

if [ "$ID_TOKEN" = "null" ] || [ -z "$ID_TOKEN" ]; then
  echo "Error: failed to get token from Dex" >&2
  echo "$RESPONSE" >&2
  exit 1
fi

echo "User:         ${USER}"
echo ""
echo "ID Token (JWT — use for Stile):"
echo "  ${ID_TOKEN}"
echo ""
echo "Access Token (opaque — for userinfo mode):"
echo "  ${ACCESS_TOKEN}"
echo ""
echo "Test commands:"
echo "  # List tools via OIDC"
echo "  curl -s -H 'Authorization: Bearer ${ID_TOKEN}' http://localhost:8080/mcp \\"
echo "    -d '{\"jsonrpc\":\"2.0\",\"method\":\"tools/list\",\"id\":1}' | jq"
echo ""
echo "  # Export for repeated use"
echo "  export STILE_TOKEN='${ID_TOKEN}'"
