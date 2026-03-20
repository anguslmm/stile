#!/usr/bin/env bash
# test-health-reload.sh — Integration test for health checks, config reload,
# and graceful shutdown (task 9).
#
# Tests that:
#   - GET /healthz returns 200 {"status":"ok"}
#   - GET /readyz returns 200 with per-upstream detail
#   - stile_upstream_health metric appears in /metrics
#   - POST /admin/reload without admin key → 403
#   - POST /admin/reload with admin key reloads config
#   - After reload with a new upstream, new tools appear in tools/list
#   - SIGTERM triggers graceful shutdown (process exits cleanly)
#
# Usage: ./scripts/test-health-reload.sh
#
# Starts fake upstreams + gateway, runs tests, then cleans up.
# Exit code 0 = all pass, 1 = failures.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DB="/tmp/stile-test-health-$$.db"
CONFIG=""
CONFIG_RELOAD=""
UPSTREAM1_PID=""
UPSTREAM2_PID=""
GATEWAY_PID=""
GATEWAY_LOG=""
PASS=0
FAIL=0

cleanup() {
    [ -n "$GATEWAY_PID" ] && kill "$GATEWAY_PID" 2>/dev/null
    [ -n "$UPSTREAM1_PID" ] && kill "$UPSTREAM1_PID" 2>/dev/null
    [ -n "$UPSTREAM2_PID" ] && kill "$UPSTREAM2_PID" 2>/dev/null
    rm -f "$DB" "$DB-shm" "$DB-wal"
    [ -n "$CONFIG" ] && rm -f "$CONFIG"
    [ -n "$CONFIG_RELOAD" ] && rm -f "$CONFIG_RELOAD"
    [ -n "$GATEWAY_LOG" ] && rm -f "$GATEWAY_LOG"
    rm -f /tmp/stile-test-bin-health
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

mcp_request() {
    local key="$1" body="$2"
    curl -s http://localhost:8083/mcp \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $key" \
        -d "$body"
}

tools_list() { mcp_request "$1" '{"jsonrpc":"2.0","method":"tools/list","id":1}'; }

# --- Setup ---

# Kill any leftover processes on our ports.
for port in 8083 9093 9094; do
    pid=$(lsof -ti ":$port" 2>/dev/null || true)
    if [ -n "$pid" ]; then
        echo "Killing stale process on :$port (pid $pid)"
        kill "$pid" 2>/dev/null || true
        sleep 1
    fi
done

echo "Building..."
go build -o /tmp/stile-test-bin-health ./cmd/gateway/ || exit 1
STILE=/tmp/stile-test-bin-health

echo "Seeding database at $DB..."
$STILE add-caller --name alice --db "$DB"
$STILE assign-role --caller alice --role dev --db "$DB"

extract_key() { grep '^  sk-' | tr -d ' '; }
ALICE_KEY=$($STILE add-key --caller alice --label "alice-key" --db "$DB" | extract_key)
echo "  alice: $ALICE_KEY"

ADMIN_KEY="test-admin-key-12345"

# Initial config: one upstream on :9093.
CONFIG=$(mktemp)
cat > "$CONFIG" <<YAML
server:
  address: ":8083"
  db_path: "$DB"
  shutdown_timeout: 5s

upstreams:
  - name: upstream-one
    transport: streamable-http
    url: http://localhost:9093/mcp

roles:
  dev:
    allowed_tools:
      - "*"
    credentials:
      upstream-one: FAKE_HTTP_TOKEN
YAML

GATEWAY_LOG=$(mktemp)

echo "Starting fake upstream 1 on :9093..."
go run ./scripts/fake-upstream.go :9093 &>/dev/null &
UPSTREAM1_PID=$!
sleep 1

echo "Starting gateway on :8083..."
ADMIN_API_KEY="$ADMIN_KEY" FAKE_HTTP_TOKEN=fake-token \
    $STILE -config "$CONFIG" >"$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

# Wait for gateway to be ready.
READY=false
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '' http://localhost:8083/healthz 2>/dev/null; then
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

echo "=== Liveness Probe ==="
HEALTHZ_BODY=$(curl -s http://localhost:8083/healthz)
HEALTHZ_CODE=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8083/healthz)
check_status_code "GET /healthz returns 200" 200 "$HEALTHZ_CODE"
check_contains "healthz body has status ok" '"status":"ok"' "$HEALTHZ_BODY"
echo ""

echo "=== Readiness Probe ==="
READYZ_BODY=$(curl -s http://localhost:8083/readyz)
READYZ_CODE=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8083/readyz)
check_status_code "GET /readyz returns 200" 200 "$READYZ_CODE"
check_contains "readyz has status ready" '"status":"ready"' "$READYZ_BODY"
check_contains "readyz has upstream-one" '"upstream-one"' "$READYZ_BODY"
check_contains "readyz has healthy field" '"healthy"' "$READYZ_BODY"
check_contains "readyz has tools field" '"tools"' "$READYZ_BODY"
echo ""

echo "=== Upstream Health Metric ==="
# Make a request first to ensure metrics are populated.
tools_list "$ALICE_KEY" > /dev/null
sleep 0.5
METRICS=$(curl -s http://localhost:8083/metrics)
check_contains "metrics has stile_upstream_health" "stile_upstream_health" "$METRICS"
check_contains "health metric has upstream-one label" 'upstream="upstream-one"' "$METRICS"
echo ""

echo "=== Admin Reload Auth ==="
# Without admin key → 403.
RELOAD_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8083/admin/reload)
check_status_code "POST /admin/reload without key → 403" 403 "$RELOAD_CODE"

# With wrong key → 403.
RELOAD_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer wrong-key" \
    http://localhost:8083/admin/reload)
check_status_code "POST /admin/reload with wrong key → 403" 403 "$RELOAD_CODE"
echo ""

echo "=== Config Reload: Add Upstream ==="

# Start a second fake upstream on :9094.
echo "Starting fake upstream 2 on :9094..."
go run ./scripts/fake-upstream.go :9094 &>/dev/null &
UPSTREAM2_PID=$!
sleep 1

# Update config file to add the second upstream.
cat > "$CONFIG" <<YAML
server:
  address: ":8083"
  db_path: "$DB"
  shutdown_timeout: 5s

upstreams:
  - name: upstream-one
    transport: streamable-http
    url: http://localhost:9093/mcp
  - name: upstream-two
    transport: streamable-http
    url: http://localhost:9094/mcp

roles:
  dev:
    allowed_tools:
      - "*"
    credentials:
      upstream-one: FAKE_HTTP_TOKEN
      upstream-two: FAKE_HTTP_TOKEN
YAML

# Trigger reload via admin endpoint.
RELOAD_BODY=$(curl -s -X POST \
    -H "Authorization: Bearer $ADMIN_KEY" \
    http://localhost:8083/admin/reload)
RELOAD_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $ADMIN_KEY" \
    http://localhost:8083/admin/reload)
check_status_code "POST /admin/reload with admin key → 200" 200 "$RELOAD_CODE"
check_contains "reload response has status ok" '"status":"ok"' "$RELOAD_BODY"
check_contains "reload shows upstream-two added" '"upstream-two"' "$RELOAD_BODY"
echo ""

echo "=== After Reload: New Upstream Tools Available ==="
# After reload, tools/list should show tools from both upstreams.
# The fake upstream exposes echo and add — both copies should be deduplicated
# by the router (first wins), but the upstream-two should be registered.
sleep 1

# Verify readyz now shows both upstreams.
READYZ_BODY=$(curl -s http://localhost:8083/readyz)
check_contains "readyz shows upstream-one after reload" '"upstream-one"' "$READYZ_BODY"
check_contains "readyz shows upstream-two after reload" '"upstream-two"' "$READYZ_BODY"

# Verify tools are still accessible.
TOOLS_RESP=$(tools_list "$ALICE_KEY")
check_contains "tools/list has echo after reload" '"echo"' "$TOOLS_RESP"
check_contains "tools/list has add after reload" '"add"' "$TOOLS_RESP"

# Verify upstream-two health metric appears.
sleep 0.5
METRICS=$(curl -s http://localhost:8083/metrics)
check_contains "health metric has upstream-two after reload" 'upstream="upstream-two"' "$METRICS"
echo ""

echo "=== Config Reload via SIGHUP ==="

# Modify config back to one upstream and send SIGHUP.
cat > "$CONFIG" <<YAML
server:
  address: ":8083"
  db_path: "$DB"
  shutdown_timeout: 5s

upstreams:
  - name: upstream-one
    transport: streamable-http
    url: http://localhost:9093/mcp

roles:
  dev:
    allowed_tools:
      - "*"
    credentials:
      upstream-one: FAKE_HTTP_TOKEN
YAML

kill -HUP "$GATEWAY_PID"
sleep 2

# After SIGHUP reload, readyz should only show upstream-one.
READYZ_BODY=$(curl -s http://localhost:8083/readyz)
check_contains "readyz has upstream-one after SIGHUP" '"upstream-one"' "$READYZ_BODY"
check_not_contains "readyz does NOT have upstream-two after SIGHUP" '"upstream-two"' "$READYZ_BODY"

# Gateway should still be serving.
HEALTHZ_CODE=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8083/healthz)
check_status_code "gateway still alive after SIGHUP" 200 "$HEALTHZ_CODE"
echo ""

echo "=== Graceful Shutdown ==="

# Send SIGTERM and verify the process exits cleanly.
kill -TERM "$GATEWAY_PID"

# Wait for the process to exit (up to 10s).
EXITED=false
for i in $(seq 1 20); do
    if ! kill -0 "$GATEWAY_PID" 2>/dev/null; then
        EXITED=true
        break
    fi
    sleep 0.5
done

if [ "$EXITED" = true ]; then
    echo "  PASS: gateway exited after SIGTERM"
    PASS=$((PASS + 1))
else
    echo "  FAIL: gateway did not exit within 10s after SIGTERM"
    FAIL=$((FAIL + 1))
fi

# Check logs for clean shutdown messages.
LOGS=$(cat "$GATEWAY_LOG")
check_contains "log shows shutting down" '"shutting down..."' "$LOGS"
check_contains "log shows shutdown complete" '"shutdown complete"' "$LOGS"

# Clear GATEWAY_PID so cleanup doesn't try to kill it again.
GATEWAY_PID=""
echo ""

# --- Summary ---

echo "============================================"
echo "Results: $PASS passed, $FAIL failed"
echo "============================================"

[ "$FAIL" -eq 0 ]
