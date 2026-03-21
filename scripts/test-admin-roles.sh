#!/usr/bin/env bash
# test-admin-roles.sh — Integration test for admin API role management
# endpoints (task 10.1).
#
# Tests that:
#   - POST /admin/callers/{name}/roles assigns a role (200)
#   - Assigning the same role twice is idempotent (no duplicate)
#   - Assigning to unknown caller returns 404
#   - Assigning empty role returns 400
#   - DELETE /admin/callers/{name}/roles/{role} removes a role (204)
#   - Deleting a role that isn't assigned returns 404
#   - GET /admin/callers/{name}/roles lists all roles
#   - GET /admin/callers/{name} includes roles in detail response
#
# Usage: ./scripts/test-admin-roles.sh
#
# Starts fake upstream + gateway, runs tests, then cleans up.
# Exit code 0 = all pass, 1 = failures.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DB="/tmp/stile-test-admin-roles-$$.db"
CONFIG=""
UPSTREAM_PID=""
GATEWAY_PID=""
GATEWAY_LOG=""
PASS=0
FAIL=0

cleanup() {
    [ -n "$GATEWAY_PID" ] && kill "$GATEWAY_PID" 2>/dev/null
    [ -n "$UPSTREAM_PID" ] && kill "$UPSTREAM_PID" 2>/dev/null
    rm -f "$DB" "$DB-shm" "$DB-wal"
    [ -n "$CONFIG" ] && rm -f "$CONFIG"
    [ -n "$GATEWAY_LOG" ] && rm -f "$GATEWAY_LOG"
    rm -f /tmp/stile-test-bin-admin-roles
    wait 2>/dev/null
    return 0
}
trap cleanup EXIT

# --- Helpers ---

check_contains() {
    local desc="$1" expected="$2" actual="$3"
    if echo "$actual" | grep -qF -- "$expected"; then
        echo "  PASS: $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $desc"
        echo "    expected to contain: $expected"
        echo "    got: $actual"
        FAIL=$((FAIL + 1))
    fi
}

check_not_contains() {
    local desc="$1" unwanted="$2" actual="$3"
    if echo "$actual" | grep -qF -- "$unwanted"; then
        echo "  FAIL: $desc"
        echo "    should NOT contain: $unwanted"
        echo "    got: $actual"
        FAIL=$((FAIL + 1))
    else
        echo "  PASS: $desc"
        PASS=$((PASS + 1))
    fi
}

check_status_code() {
    local desc="$1" expected="$2" actual="$3"
    if [ "$actual" -eq "$expected" ]; then
        echo "  PASS: $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $desc"
        echo "    expected HTTP status: $expected"
        echo "    got: $actual"
        FAIL=$((FAIL + 1))
    fi
}

admin_request() {
    local method="$1" path="$2" body="$3"
    if [ -n "$body" ]; then
        curl -s -X "$method" "http://localhost:8084$path" \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer $ADMIN_KEY" \
            -d "$body"
    else
        curl -s -X "$method" "http://localhost:8084$path" \
            -H "Authorization: Bearer $ADMIN_KEY"
    fi
}

admin_status() {
    local method="$1" path="$2" body="$3"
    if [ -n "$body" ]; then
        curl -s -o /dev/null -w '%{http_code}' -X "$method" "http://localhost:8084$path" \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer $ADMIN_KEY" \
            -d "$body"
    else
        curl -s -o /dev/null -w '%{http_code}' -X "$method" "http://localhost:8084$path" \
            -H "Authorization: Bearer $ADMIN_KEY"
    fi
}

# --- Setup ---

# Kill any leftover processes on our ports.
for port in 8084 9095; do
    pid=$(lsof -ti ":$port" 2>/dev/null || true)
    if [ -n "$pid" ]; then
        echo "Killing stale process on :$port (pid $pid)"
        kill "$pid" 2>/dev/null || true
        sleep 1
    fi
done

echo "Building..."
go build -o /tmp/stile-test-bin-admin-roles ./cmd/gateway/ || exit 1
STILE=/tmp/stile-test-bin-admin-roles

echo "Seeding database at $DB..."
$STILE add-caller --name angus --db "$DB"
$STILE add-caller --name alice --db "$DB"

extract_key() { grep '^  sk-' | tr -d ' '; }
$STILE add-key --caller angus --label "laptop" --db "$DB" > /dev/null

ADMIN_KEY="test-admin-key-roles-12345"

CONFIG=$(mktemp)
cat > "$CONFIG" <<YAML
server:
  address: ":8084"
  db_path: "$DB"

upstreams:
  - name: fake-http
    transport: streamable-http
    url: http://localhost:9095/mcp

roles:
  dev:
    allowed_tools:
      - "*"
    credentials:
      fake-http: FAKE_HTTP_TOKEN
YAML

GATEWAY_LOG=$(mktemp)

echo "Starting fake upstream on :9095..."
go run ./scripts/fake-upstream.go :9095 &>/dev/null &
UPSTREAM_PID=$!
sleep 1

echo "Starting gateway on :8084..."
ADMIN_API_KEY="$ADMIN_KEY" FAKE_HTTP_TOKEN=fake-token \
    $STILE -config "$CONFIG" >"$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

# Wait for gateway to be ready.
READY=false
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '' http://localhost:8084/healthz 2>/dev/null; then
        READY=true
        break
    fi
    sleep 0.5
done

if [ "$READY" = false ]; then
    echo "ERROR: Gateway failed to start. Logs:"
    cat "$GATEWAY_LOG"
    exit 1
fi
echo "Gateway ready."
echo ""

# --- Tests ---

echo "=== Assign Role ==="

RESP=$(admin_request POST /admin/callers/angus/roles '{"role":"dev"}')
CODE=$(admin_status POST /admin/callers/angus/roles '{"role":"dev"}')
check_status_code "POST assign role → 200" 200 "$CODE"
check_contains "response has caller name" '"name":"angus"' "$RESP"
check_contains "response has role dev" '"dev"' "$RESP"
echo ""

echo "=== Assign Role Idempotent ==="

# Assign same role again — should succeed with no duplicate.
RESP=$(admin_request POST /admin/callers/angus/roles '{"role":"dev"}')
CODE=$(admin_status POST /admin/callers/angus/roles '{"role":"dev"}')
check_status_code "POST assign same role again → 200" 200 "$CODE"

# Count occurrences of "dev" in the roles array — should be exactly 1.
DEV_COUNT=$(echo "$RESP" | grep -o '"dev"' | wc -l | tr -d ' ')
if [ "$DEV_COUNT" -eq 1 ]; then
    echo "  PASS: no duplicate role"
    PASS=$((PASS + 1))
else
    echo "  FAIL: expected 1 occurrence of dev, got $DEV_COUNT"
    echo "    response: $RESP"
    FAIL=$((FAIL + 1))
fi
echo ""

echo "=== Assign Role Unknown Caller ==="

CODE=$(admin_status POST /admin/callers/nobody/roles '{"role":"dev"}')
check_status_code "POST unknown caller → 404" 404 "$CODE"
echo ""

echo "=== Assign Role Empty ==="

CODE=$(admin_status POST /admin/callers/angus/roles '{"role":""}')
check_status_code "POST empty role → 400" 400 "$CODE"
echo ""

echo "=== Assign Multiple Roles ==="

admin_request POST /admin/callers/angus/roles '{"role":"prod"}' > /dev/null

RESP=$(admin_request GET /admin/callers/angus/roles "")
CODE=$(admin_status GET /admin/callers/angus/roles "")
check_status_code "GET list roles → 200" 200 "$CODE"
check_contains "has dev role" '"dev"' "$RESP"
check_contains "has prod role" '"prod"' "$RESP"
echo ""

echo "=== List Roles ==="

RESP=$(admin_request GET /admin/callers/angus/roles "")
CODE=$(admin_status GET /admin/callers/angus/roles "")
check_status_code "GET roles → 200" 200 "$CODE"
check_contains "roles response has roles key" '"roles"' "$RESP"
check_contains "roles has dev" '"dev"' "$RESP"
check_contains "roles has prod" '"prod"' "$RESP"
echo ""

echo "=== List Roles Unknown Caller ==="

CODE=$(admin_status GET /admin/callers/nobody/roles "")
check_status_code "GET roles unknown caller → 404" 404 "$CODE"
echo ""

echo "=== Unassign Role ==="

CODE=$(admin_status DELETE /admin/callers/angus/roles/prod "")
check_status_code "DELETE role → 204" 204 "$CODE"

# Verify role is gone.
RESP=$(admin_request GET /admin/callers/angus/roles "")
check_contains "still has dev" '"dev"' "$RESP"
check_not_contains "prod is gone" '"prod"' "$RESP"
echo ""

echo "=== Unassign Role Not Assigned ==="

CODE=$(admin_status DELETE /admin/callers/angus/roles/nonexistent "")
check_status_code "DELETE unassigned role → 404" 404 "$CODE"
echo ""

echo "=== Caller Detail Includes Roles ==="

RESP=$(admin_request GET /admin/callers/angus "")
CODE=$(admin_status GET /admin/callers/angus "")
check_status_code "GET caller detail → 200" 200 "$CODE"
check_contains "detail has roles key" '"roles"' "$RESP"
check_contains "detail has dev role" '"dev"' "$RESP"
check_contains "detail has keys" '"keys"' "$RESP"
check_contains "detail has name" '"name":"angus"' "$RESP"
echo ""

echo "=== Caller Detail No Roles ==="

# Alice has no roles — should return empty array.
RESP=$(admin_request GET /admin/callers/alice "")
check_contains "detail has roles key for alice" '"roles"' "$RESP"
check_contains "alice roles is empty array" '"roles":[]' "$RESP"
echo ""

# --- Summary ---

echo "============================================"
echo "Results: $PASS passed, $FAIL failed"
echo "============================================"

[ "$FAIL" -eq 0 ]
