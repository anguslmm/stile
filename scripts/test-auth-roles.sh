#!/usr/bin/env bash
# test-auth-roles.sh — Integration test for role-based access control.
#
# Tests that:
#   - Unauthenticated requests are rejected
#   - Single-role callers see only their role's tools
#   - Multi-role callers see the union of all assigned roles' tools
#   - Wildcard roles see everything
#   - Access-denied is enforced on tools/call
#
# Usage: ./scripts/test-auth-roles.sh
#
# Starts fake upstream + gateway, seeds the DB, runs tests, then cleans up.
# Exit code 0 = all pass, 1 = failures.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DB="/tmp/stile-test-roles-$$.db"
CONFIG=""
UPSTREAM_PID=""
GATEWAY_PID=""
PASS=0
FAIL=0

cleanup() {
    [ -n "$GATEWAY_PID" ] && kill "$GATEWAY_PID" 2>/dev/null
    [ -n "$UPSTREAM_PID" ] && kill "$UPSTREAM_PID" 2>/dev/null
    rm -f "$DB" "$DB-shm" "$DB-wal"
    [ -n "$CONFIG" ] && rm -f "$CONFIG"
    rm -f /tmp/stile-test-bin
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

mcp_request() {
    local key="$1" body="$2"
    if [ -n "$key" ]; then
        curl -s http://localhost:8080/mcp \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer $key" \
            -d "$body"
    else
        curl -s http://localhost:8080/mcp \
            -H "Content-Type: application/json" \
            -d "$body"
    fi
}

tools_list() { mcp_request "$1" '{"jsonrpc":"2.0","method":"tools/list","id":1}'; }

tools_call() {
    local key="$1" tool="$2" args="$3"
    mcp_request "$key" "{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args},\"id\":1}"
}

# --- Setup ---

# Kill any leftover processes on our ports.
for port in 8080 9090; do
    pid=$(lsof -ti ":$port" 2>/dev/null || true)
    if [ -n "$pid" ]; then
        echo "Killing stale process on :$port (pid $pid)"
        kill "$pid" 2>/dev/null || true
        sleep 1
    fi
done

echo "Building..."
go build -o /tmp/stile-test-bin ./cmd/gateway/ || exit 1
STILE=/tmp/stile-test-bin

echo "Seeding database at $DB..."
$STILE add-caller --name alice --db "$DB"
$STILE add-caller --name bob --db "$DB"
$STILE add-caller --name charlie --db "$DB"

$STILE assign-role --caller alice --role http-only --db "$DB"
$STILE assign-role --caller alice --role stdio-only --db "$DB"
$STILE assign-role --caller bob --role full-access --db "$DB"
$STILE assign-role --caller charlie --role http-only --db "$DB"

extract_key() { grep '^  sk-' | tr -d ' '; }

ALICE_KEY=$($STILE add-key --caller alice --label "alice-key" --db "$DB" | extract_key)
BOB_KEY=$($STILE add-key --caller bob --label "bob-key" --db "$DB" | extract_key)
CHARLIE_KEY=$($STILE add-key --caller charlie --label "charlie-key" --db "$DB" | extract_key)

echo "  alice:   $ALICE_KEY"
echo "  bob:     $BOB_KEY"
echo "  charlie: $CHARLIE_KEY"

# Write a temporary config pointing at our DB.
CONFIG=$(mktemp)
cat > "$CONFIG" <<YAML
server:
  address: ":8080"
  db_path: "$DB"

upstreams:
  - name: fake-http
    transport: streamable-http
    url: http://localhost:9090/mcp

  - name: local-stdio
    transport: stdio
    command: ["go", "run", "./internal/transport/testdata/mock_stdio_server.go"]

roles:
  http-only:
    allowed_tools:
      - "echo"
      - "add"
    credentials:
      fake-http: FAKE_HTTP_TOKEN
  stdio-only:
    allowed_tools:
      - "test_echo"
    credentials:
      local-stdio: STDIO_TOKEN
  full-access:
    allowed_tools:
      - "*"
    credentials:
      fake-http: FAKE_HTTP_TOKEN
      local-stdio: STDIO_TOKEN
YAML

GATEWAY_LOG=$(mktemp)

echo "Starting fake upstream on :9090..."
go run ./scripts/fake-upstream.go &>/dev/null &
UPSTREAM_PID=$!
sleep 1

echo "Starting gateway on :8080..."
FAKE_HTTP_TOKEN=fake-token STDIO_TOKEN=stdio-token \
    $STILE -config "$CONFIG" >"$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

# Wait for gateway to be ready (up to 15s for go run compilation).
READY=false
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '' http://localhost:8080/mcp -X POST \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"tools/list","id":0}' 2>/dev/null; then
        READY=true
        break
    fi
    sleep 0.5
done

if [ "$READY" = false ]; then
    echo "ERROR: Gateway failed to start. Logs:"
    cat "$GATEWAY_LOG"
    rm -f "$GATEWAY_LOG"
    exit 1
fi
echo "Gateway ready."
rm -f "$GATEWAY_LOG"
echo ""

# --- Tests ---

echo "=== Unauthenticated ==="
RESP=$(tools_list "")
check_contains "no auth → unauthorized" '"unauthorized"' "$RESP"

RESP=$(mcp_request "" '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"message":"x"}},"id":1}')
check_contains "no auth tools/call → unauthorized" '"unauthorized"' "$RESP"
echo ""

echo "=== Charlie (single role: http-only) ==="
RESP=$(tools_list "$CHARLIE_KEY")
check_contains     "sees echo"              '"name":"echo"'    "$RESP"
check_contains     "sees add"               '"name":"add"'     "$RESP"
check_not_contains "does NOT see test_echo" 'test_echo'        "$RESP"

RESP=$(tools_call "$CHARLIE_KEY" "echo" '{"message":"hi charlie"}')
check_contains "can call echo" "hi charlie" "$RESP"

RESP=$(tools_call "$CHARLIE_KEY" "add" '{"a":2,"b":3}')
check_contains "can call add" "5" "$RESP"

RESP=$(tools_call "$CHARLIE_KEY" "test_echo" '{"message":"denied"}')
check_contains "cannot call test_echo → access denied" '"access denied"' "$RESP"
echo ""

echo "=== Alice (overlapping roles: http-only + stdio-only) ==="
RESP=$(tools_list "$ALICE_KEY")
check_contains "sees echo"      '"name":"echo"'      "$RESP"
check_contains "sees add"       '"name":"add"'        "$RESP"
check_contains "sees test_echo" '"name":"test_echo"'  "$RESP"

RESP=$(tools_call "$ALICE_KEY" "echo" '{"message":"hi alice"}')
check_contains "can call echo (http-only role)" "hi alice" "$RESP"

RESP=$(tools_call "$ALICE_KEY" "test_echo" '{"message":"hi alice stdio"}')
check_contains "can call test_echo (stdio-only role)" "hi alice stdio" "$RESP"

RESP=$(tools_call "$ALICE_KEY" "add" '{"a":10,"b":20}')
check_contains "can call add (http-only role)" "30" "$RESP"
echo ""

echo "=== Bob (single role: full-access wildcard) ==="
RESP=$(tools_list "$BOB_KEY")
check_contains "sees echo"      '"name":"echo"'      "$RESP"
check_contains "sees add"       '"name":"add"'        "$RESP"
check_contains "sees test_echo" '"name":"test_echo"'  "$RESP"

RESP=$(tools_call "$BOB_KEY" "echo" '{"message":"hi bob"}')
check_contains "can call echo" "hi bob" "$RESP"

RESP=$(tools_call "$BOB_KEY" "add" '{"a":3,"b":7}')
check_contains "can call add" "10" "$RESP"

RESP=$(tools_call "$BOB_KEY" "test_echo" '{"message":"hi bob stdio"}')
check_contains "can call test_echo" "hi bob stdio" "$RESP"
echo ""

echo "=== Invalid key ==="
RESP=$(tools_list "sk-bogus-key")
check_contains "invalid key → unauthorized" '"unauthorized"' "$RESP"

RESP=$(mcp_request "sk-bogus-key" '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"message":"x"}},"id":1}')
check_contains "invalid key tools/call → unauthorized" '"unauthorized"' "$RESP"
echo ""

# --- Summary ---

echo "============================================"
echo "Results: $PASS passed, $FAIL failed"
echo "============================================"

[ "$FAIL" -eq 0 ]
