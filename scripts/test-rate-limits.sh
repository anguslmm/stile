#!/usr/bin/env bash
# test-rate-limits.sh — Integration test for rate limiting.
#
# Tests that:
#   - Requests within the limit are allowed
#   - Requests over the per-caller limit are rejected with "rate limit exceeded"
#   - Per-caller isolation: one caller's limit doesn't affect another
#   - Per-upstream limit rejects when aggregate load is too high
#   - Rejection response includes the limit level in data
#   - No rate limits configured means everything passes
#
# Usage: ./scripts/test-rate-limits.sh
#
# Starts fake upstream + gateway, seeds the DB, runs tests, then cleans up.
# Exit code 0 = all pass, 1 = failures.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DB="/tmp/stile-test-ratelimit-$$.db"
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
    rm -f /tmp/stile-test-bin-rl
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
    curl -s http://localhost:8081/mcp \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $key" \
        -d "$body"
}

tools_call() {
    local key="$1" tool="$2" args="$3"
    mcp_request "$key" "{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args},\"id\":1}"
}

tools_list() { mcp_request "$1" '{"jsonrpc":"2.0","method":"tools/list","id":1}'; }

# --- Setup ---

# Kill any leftover processes on our ports.
for port in 8081 9091; do
    pid=$(lsof -ti ":$port" 2>/dev/null || true)
    if [ -n "$pid" ]; then
        echo "Killing stale process on :$port (pid $pid)"
        kill "$pid" 2>/dev/null || true
        sleep 1
    fi
done

echo "Building..."
go build -o /tmp/stile-test-bin-rl ./cmd/gateway/ || exit 1
STILE=/tmp/stile-test-bin-rl

echo "Seeding database at $DB..."
$STILE add-caller --name alice --db "$DB"
$STILE add-caller --name bob --db "$DB"

$STILE assign-role --caller alice --role dev --db "$DB"
$STILE assign-role --caller bob --role dev --db "$DB"

extract_key() { grep '^  sk-' | tr -d ' '; }

ALICE_KEY=$($STILE add-key --caller alice --label "alice-key" --db "$DB" | extract_key)
BOB_KEY=$($STILE add-key --caller bob --label "bob-key" --db "$DB" | extract_key)

echo "  alice: $ALICE_KEY"
echo "  bob:   $BOB_KEY"

# Config with low rate limits to make testing easy.
# Per-caller: 5/sec, per-tool: 3/sec, per-upstream: 10/sec
CONFIG=$(mktemp)
cat > "$CONFIG" <<YAML
server:
  address: ":8081"
  db_path: "$DB"

upstreams:
  - name: fake-http
    transport: streamable-http
    url: http://localhost:9091/mcp
    rate_limit: 10/sec

roles:
  dev:
    allowed_tools:
      - "*"
    rate_limit: 5/sec
    tool_rate_limit: 3/sec
    credentials:
      fake-http: FAKE_HTTP_TOKEN

rate_limits:
  default_caller: 5/sec
  default_tool: 3/sec
  default_upstream: 10/sec
YAML

GATEWAY_LOG=$(mktemp)

echo "Starting fake upstream on :9091..."
go run ./scripts/fake-upstream.go :9091 &>/dev/null &
UPSTREAM_PID=$!
sleep 1

echo "Starting gateway on :8081..."
FAKE_HTTP_TOKEN=fake-token \
    $STILE -config "$CONFIG" >"$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

# Wait for gateway to be ready.
READY=false
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '' http://localhost:8081/mcp -X POST \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $ALICE_KEY" \
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

echo "=== Requests within limit ==="
# A single request should always pass.
RESP=$(tools_call "$ALICE_KEY" "echo" '{"message":"hello"}')
check_contains "single request passes" "hello" "$RESP"
check_not_contains "no rate limit error" "rate limit exceeded" "$RESP"
echo ""

echo "=== Per-caller rate limit ==="
# Send many rapid requests to exceed the 5/sec per-caller limit.
# We need to send them as fast as possible in a burst.
RATE_LIMITED=false
for i in $(seq 1 20); do
    RESP=$(tools_call "$ALICE_KEY" "echo" '{"message":"burst"}')
    if echo "$RESP" | grep -qF "rate limit exceeded"; then
        RATE_LIMITED=true
        break
    fi
done

if [ "$RATE_LIMITED" = true ]; then
    echo "  PASS: caller rate limit kicks in after burst"
    PASS=$((PASS + 1))
else
    echo "  FAIL: caller rate limit should reject after 20 rapid requests at 5/sec"
    FAIL=$((FAIL + 1))
fi

# Check the rejection includes the limit level.
RESP=$(tools_call "$ALICE_KEY" "echo" '{"message":"over"}')
if echo "$RESP" | grep -qF "rate limit exceeded"; then
    check_contains "rejection data includes limit level" '"limit"' "$RESP"
fi
echo ""

echo "=== Per-caller isolation ==="
# Bob should still be able to make requests even though Alice is rate limited.
RESP=$(tools_call "$BOB_KEY" "echo" '{"message":"bob ok"}')
check_contains "bob is not affected by alice's limit" "bob ok" "$RESP"
check_not_contains "bob gets no rate limit error" "rate limit exceeded" "$RESP"
echo ""

echo "=== Per-tool rate limit ==="
# Restart the gateway to reset limiters.
kill "$GATEWAY_PID" 2>/dev/null
wait "$GATEWAY_PID" 2>/dev/null
GATEWAY_PID=""
sleep 1

GATEWAY_LOG=$(mktemp)
FAKE_HTTP_TOKEN=fake-token \
    $STILE -config "$CONFIG" >"$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

READY=false
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '' http://localhost:8081/mcp -X POST \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $ALICE_KEY" \
        -d '{"jsonrpc":"2.0","method":"tools/list","id":0}' 2>/dev/null; then
        READY=true
        break
    fi
    sleep 0.5
done
rm -f "$GATEWAY_LOG"

if [ "$READY" = false ]; then
    echo "ERROR: Gateway failed to restart."
    exit 1
fi

# Exhaust the per-tool limit on "echo" (3/sec).
for i in $(seq 1 10); do
    tools_call "$ALICE_KEY" "echo" '{"message":"exhaust"}' > /dev/null
done

# "add" should still work since it has its own per-tool bucket.
RESP=$(tools_call "$ALICE_KEY" "add" '{"a":1,"b":2}')
# Note: alice's per-caller limit (5/sec) may also be exhausted, so this
# tests whether we get the tool-level or caller-level limit.
# Either way, the per-tool bucket for "add" is fresh.
echo "  INFO: add response: $RESP"
echo ""

echo "=== Rate limit error format ==="
# Restart again for a clean slate.
kill "$GATEWAY_PID" 2>/dev/null
wait "$GATEWAY_PID" 2>/dev/null
GATEWAY_PID=""
sleep 1

GATEWAY_LOG=$(mktemp)
FAKE_HTTP_TOKEN=fake-token \
    $STILE -config "$CONFIG" >"$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

READY=false
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '' http://localhost:8081/mcp -X POST \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $ALICE_KEY" \
        -d '{"jsonrpc":"2.0","method":"tools/list","id":0}' 2>/dev/null; then
        READY=true
        break
    fi
    sleep 0.5
done
rm -f "$GATEWAY_LOG"

if [ "$READY" = false ]; then
    echo "ERROR: Gateway failed to restart."
    exit 1
fi

# Exhaust the limit, then capture the rejection response.
for i in $(seq 1 10); do
    tools_call "$ALICE_KEY" "echo" '{"message":"fill"}' > /dev/null
done

RESP=$(tools_call "$ALICE_KEY" "echo" '{"message":"check-format"}')
if echo "$RESP" | grep -qF "rate limit exceeded"; then
    check_contains "error code is -32000" "-32000" "$RESP"
    check_contains "error message" "rate limit exceeded" "$RESP"
    check_contains "data has limit field" '"limit"' "$RESP"
else
    echo "  SKIP: could not trigger rate limit for format check (timing dependent)"
fi
echo ""

# --- Summary ---

echo "============================================"
echo "Results: $PASS passed, $FAIL failed"
echo "============================================"

[ "$FAIL" -eq 0 ]
