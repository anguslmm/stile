#!/usr/bin/env bash
# Test the outbound OAuth flow end-to-end.
# Requires: docker-compose.scaling.yml running.
#
# Usage:
#   export ADMIN_API_KEY="pick-a-secret"
#   docker compose -f docker-compose.scaling.yml up --build -d
#   scripts/test-oauth-flow.sh

set -euo pipefail

STILE_URL="${STILE_URL:-http://localhost:8080}"
ADMIN_API_KEY="${ADMIN_API_KEY:?ADMIN_API_KEY must be set}"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

echo "=== Outbound OAuth Flow E2E Tests ==="
echo ""

# 1. Get OIDC token for alice (password grant via Dex).
echo "1. Getting OIDC token for alice@example.com..."
ALICE_RESPONSE=$(curl -sf -X POST "${STILE_URL%:8080}:5556/dex/token" \
  -d grant_type=password \
  -d "username=alice@example.com" \
  -d "password=password" \
  -d "client_id=stile" \
  -d "client_secret=stile-secret" \
  -d "scope=openid email profile" 2>/dev/null || echo "")

if [ -z "$ALICE_RESPONSE" ]; then
  echo "  Could not get OIDC token from Dex. Is docker-compose running?"
  exit 1
fi

ALICE_TOKEN=$(echo "$ALICE_RESPONSE" | jq -r '.id_token')
if [ "$ALICE_TOKEN" = "null" ] || [ -z "$ALICE_TOKEN" ]; then
  echo "  Failed to extract id_token for alice."
  echo "  Response: $ALICE_RESPONSE"
  exit 1
fi
pass "Got OIDC token for alice"

# 2. Insert a mock upstream token via admin API (escape hatch).
echo ""
echo "2. Inserting mock token for alice via admin API..."
PUT_RESULT=$(curl -sf -X PUT "${STILE_URL}/admin/connections/mock" \
  -H "Authorization: Bearer ${ADMIN_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"caller": "alice@example.com", "access_token": "test-token-for-alice"}' 2>/dev/null || echo "")

if echo "$PUT_RESULT" | jq -e '.status == "ok"' > /dev/null 2>&1; then
  pass "Inserted mock token for alice"
else
  fail "Could not insert mock token: $PUT_RESULT"
fi

# 3. Verify the connection shows up in the admin API.
echo ""
echo "3. Listing connections for alice..."
LIST_RESULT=$(curl -sf "${STILE_URL}/admin/connections?caller=alice@example.com" \
  -H "Authorization: Bearer ${ADMIN_API_KEY}" 2>/dev/null || echo "")

if echo "$LIST_RESULT" | jq -e '.connections[] | select(.provider == "mock" and .connected == true)' > /dev/null 2>&1; then
  pass "Connection listed as connected"
else
  fail "Connection not listed: $LIST_RESULT"
fi

# 4. Call the mock upstream's whoami tool — verify the injected token.
echo ""
echo "4. Calling whoami tool (should receive per-user token)..."
WHOAMI_RESULT=$(curl -sf "${STILE_URL}/mcp" \
  -H "Authorization: Bearer ${ALICE_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"whoami"},"id":1}' 2>/dev/null || echo "")

if echo "$WHOAMI_RESULT" | jq -e '.result.content[0].text == "Bearer test-token-for-alice"' > /dev/null 2>&1; then
  pass "Upstream received per-user token (not OIDC token)"
else
  fail "Unexpected whoami result: $WHOAMI_RESULT"
fi

# 5. Test missing connection — bob hasn't connected.
echo ""
echo "5. Getting OIDC token for bob@example.com..."
BOB_RESPONSE=$(curl -sf -X POST "${STILE_URL%:8080}:5556/dex/token" \
  -d grant_type=password \
  -d "username=bob@example.com" \
  -d "password=password" \
  -d "client_id=stile" \
  -d "client_secret=stile-secret" \
  -d "scope=openid email profile" 2>/dev/null || echo "")

BOB_TOKEN=$(echo "$BOB_RESPONSE" | jq -r '.id_token')

echo "   Calling whoami as bob (no connection)..."
BOB_RESULT=$(curl -sf "${STILE_URL}/mcp" \
  -H "Authorization: Bearer ${BOB_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"whoami"},"id":1}' 2>/dev/null || echo "")

if echo "$BOB_RESULT" | jq -e '.error.message' > /dev/null 2>&1; then
  pass "Missing connection returns error for bob"
else
  fail "Expected error for bob, got: $BOB_RESULT"
fi

# 6. Delete alice's connection and verify it's gone.
echo ""
echo "6. Deleting alice's mock connection..."
DEL_RESULT=$(curl -sf -o /dev/null -w "%{http_code}" -X DELETE \
  "${STILE_URL}/admin/connections/mock?caller=alice@example.com" \
  -H "Authorization: Bearer ${ADMIN_API_KEY}" 2>/dev/null || echo "")

if [ "$DEL_RESULT" = "204" ]; then
  pass "Deleted alice's connection"
else
  fail "Delete returned status $DEL_RESULT"
fi

# Summary
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

echo ""
echo "Manual browser test:"
echo "  1. Open: ${STILE_URL}/admin/ui/connections"
echo "  2. Look up alice@example.com"
echo "  3. Click 'Connect' for the mock provider"
echo "  4. Mock provider auto-approves and redirects back"
echo "  5. Verify connection shows as active"
echo "  6. Re-run whoami — should now work for alice"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
