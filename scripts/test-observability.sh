#!/usr/bin/env bash
# test-observability.sh — Integration test for observability features (task 8).
#
# Tests that:
#   - Structured JSON logs are emitted with expected fields
#   - GET /metrics returns Prometheus exposition format with stile_* metrics
#   - Metrics increment after requests (requests_total, request_duration)
#   - Rate limit rejection is counted in stile_rate_limit_rejections_total
#   - Audit log entries are written to SQLite when enabled
#   - Debug-level logs are suppressed at info level
#
# Usage: ./scripts/test-observability.sh
#
# Starts fake upstream + gateway, runs tests, then cleans up.
# Exit code 0 = all pass, 1 = failures.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DB="/tmp/stile-test-obs-$$.db"
AUDIT_DB="/tmp/stile-test-audit-$$.db"
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
    rm -f "$AUDIT_DB" "$AUDIT_DB-shm" "$AUDIT_DB-wal"
    [ -n "$CONFIG" ] && rm -f "$CONFIG"
    [ -n "$GATEWAY_LOG" ] && rm -f "$GATEWAY_LOG"
    rm -f /tmp/stile-test-bin-obs
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

check_exit_code() {
    local desc="$1" expected="$2" actual="$3"
    if [ "$actual" -eq "$expected" ]; then
        echo "  PASS: $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $desc"
        echo "    expected exit code: $expected"
        echo "    got: $actual"
        FAIL=$((FAIL + 1))
    fi
}

mcp_request() {
    local key="$1" body="$2"
    curl -s http://localhost:8082/mcp \
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
for port in 8082 9092; do
    pid=$(lsof -ti ":$port" 2>/dev/null || true)
    if [ -n "$pid" ]; then
        echo "Killing stale process on :$port (pid $pid)"
        kill "$pid" 2>/dev/null || true
        sleep 1
    fi
done

echo "Building..."
go build -o /tmp/stile-test-bin-obs ./cmd/gateway/ || exit 1
STILE=/tmp/stile-test-bin-obs

echo "Seeding database at $DB..."
$STILE add-caller --name alice --db "$DB"
$STILE assign-role --caller alice --role dev --db "$DB"

extract_key() { grep '^  sk-' | tr -d ' '; }
ALICE_KEY=$($STILE add-key --caller alice --label "alice-key" --db "$DB" | extract_key)
echo "  alice: $ALICE_KEY"

# Config with logging, metrics, audit, and a tight rate limit for testing.
CONFIG=$(mktemp)
cat > "$CONFIG" <<YAML
server:
  address: ":8082"
  db_path: "$DB"

logging:
  level: info
  format: json

audit:
  enabled: true
  database: "$AUDIT_DB"

upstreams:
  - name: fake-http
    transport: streamable-http
    url: http://localhost:9092/mcp

roles:
  dev:
    allowed_tools:
      - "*"
    rate_limit: 3/sec
    tool_rate_limit: 2/sec
    credentials:
      fake-http: FAKE_HTTP_TOKEN
YAML

GATEWAY_LOG=$(mktemp)

echo "Starting fake upstream on :9092..."
go run ./scripts/fake-upstream.go :9092 &>/dev/null &
UPSTREAM_PID=$!
sleep 1

echo "Starting gateway on :8082..."
FAKE_HTTP_TOKEN=fake-token \
    $STILE -config "$CONFIG" >"$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

# Wait for gateway to be ready.
READY=false
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w '' http://localhost:8082/mcp -X POST \
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
    exit 1
fi
echo "Gateway ready."
echo ""

# --- Tests ---

echo "=== Structured Logging ==="

# Make a request to generate log output.
tools_call "$ALICE_KEY" "echo" '{"message":"log-test"}' > /dev/null
sleep 0.5

LOGS=$(cat "$GATEWAY_LOG")

# Check that JSON structured log lines are present with expected fields.
LOG_LINE=$(echo "$LOGS" | grep '"request handled"' | tail -1)
check_contains "log has caller field" '"caller"' "$LOG_LINE"
check_contains "log has method field" '"method"' "$LOG_LINE"
check_contains "log has tool field" '"tool"' "$LOG_LINE"
check_contains "log has upstream field" '"upstream"' "$LOG_LINE"
check_contains "log has latency_ms field" '"latency_ms"' "$LOG_LINE"
check_contains "log has status field" '"status"' "$LOG_LINE"
check_contains "log caller is alice" '"alice"' "$LOG_LINE"
check_contains "log tool is echo" '"echo"' "$LOG_LINE"
check_contains "log status is ok" '"ok"' "$LOG_LINE"

# Check lifecycle log: server start.
check_contains "startup log present" '"stile listening"' "$LOGS"
check_contains "config loaded log" '"config loaded"' "$LOGS"
check_contains "audit enabled log" '"audit logging enabled"' "$LOGS"
echo ""

echo "=== Debug suppressed at info level ==="
# Debug-level entries (like rate limit decisions) should NOT appear at info level.
check_not_contains "no debug entries at info level" '"level":"DEBUG"' "$LOGS"
echo ""

echo "=== Error Logging ==="
# Make a request for an unknown tool to generate an error log.
tools_call "$ALICE_KEY" "nonexistent-tool" '{}' > /dev/null
sleep 0.5

LOGS=$(cat "$GATEWAY_LOG")
ERROR_LOG=$(echo "$LOGS" | grep '"request handled"' | grep '"error"' | tail -1)
check_contains "error log has status error" '"error"' "$ERROR_LOG"
echo ""

echo "=== Prometheus Metrics Endpoint ==="

METRICS=$(curl -s http://localhost:8082/metrics)
check_exit_code "GET /metrics succeeds" 0 $?
check_contains "has stile_requests_total" "stile_requests_total" "$METRICS"
check_contains "has stile_request_duration_seconds" "stile_request_duration_seconds" "$METRICS"
check_contains "has stile_tool_cache_refresh_total" "stile_tool_cache_refresh_total" "$METRICS"
# Note: stile_rate_limit_rejections_total only appears after first increment,
# so we check it in the "Rate Limit Rejection Metric" section below.

# Check that our earlier request was counted.
check_contains "metrics have caller=alice label" 'caller="alice"' "$METRICS"
check_contains "metrics have tool=echo label" 'tool="echo"' "$METRICS"
check_contains "metrics have status=ok label" 'status="ok"' "$METRICS"
check_contains "metrics have upstream=fake-http label" 'upstream="fake-http"' "$METRICS"
echo ""

echo "=== Metrics Increment After Request ==="

# Capture current request count.
BEFORE=$(curl -s http://localhost:8082/metrics | grep 'stile_requests_total{' | grep 'tool="add"' | grep 'status="ok"' || echo "0")

# Make a new request.
tools_call "$ALICE_KEY" "add" '{"a":1,"b":2}' > /dev/null
sleep 0.2

AFTER=$(curl -s http://localhost:8082/metrics | grep 'stile_requests_total{' | grep 'tool="add"' | grep 'status="ok"' || echo "0")

if [ "$BEFORE" != "$AFTER" ]; then
    echo "  PASS: request counter incremented after add call"
    PASS=$((PASS + 1))
else
    echo "  FAIL: request counter did not increment"
    echo "    before: $BEFORE"
    echo "    after:  $AFTER"
    FAIL=$((FAIL + 1))
fi

# Check duration histogram has observations.
HIST=$(curl -s http://localhost:8082/metrics | grep 'stile_request_duration_seconds_count{' | grep 'tool="echo"' || echo "")
if [ -n "$HIST" ]; then
    echo "  PASS: duration histogram has observations"
    PASS=$((PASS + 1))
else
    echo "  FAIL: duration histogram has no observations"
    FAIL=$((FAIL + 1))
fi
echo ""

echo "=== Rate Limit Rejection Metric ==="

# Exhaust the rate limit (2/sec per-tool) to trigger rejections.
for i in $(seq 1 10); do
    tools_call "$ALICE_KEY" "echo" '{"message":"exhaust"}' > /dev/null
done
sleep 0.2

RL_METRIC=$(curl -s http://localhost:8082/metrics | grep 'stile_rate_limit_rejections_total{' || echo "")
if [ -n "$RL_METRIC" ]; then
    echo "  PASS: rate limit rejection counter present"
    PASS=$((PASS + 1))
    check_contains "rejection metric has caller label" 'caller="alice"' "$RL_METRIC"
else
    echo "  FAIL: rate limit rejection counter not found"
    FAIL=$((FAIL + 1))
fi
echo ""

echo "=== Tool Cache Refresh Metric ==="

CACHE_METRIC=$(curl -s http://localhost:8082/metrics | grep 'stile_tool_cache_refresh_total{' || echo "")
if [ -n "$CACHE_METRIC" ]; then
    echo "  PASS: tool cache refresh counter present"
    PASS=$((PASS + 1))
    check_contains "cache refresh has upstream label" 'upstream="fake-http"' "$CACHE_METRIC"
    check_contains "cache refresh has success status" 'status="success"' "$CACHE_METRIC"
else
    echo "  FAIL: tool cache refresh counter not found"
    FAIL=$((FAIL + 1))
fi
echo ""

echo "=== Audit Log ==="

# Check that audit entries were written to the SQLite database.
if [ ! -f "$AUDIT_DB" ]; then
    echo "  FAIL: audit database file does not exist"
    FAIL=$((FAIL + 1))
else
    # Count rows in the audit_log table.
    ROW_COUNT=$(sqlite3 "$AUDIT_DB" "SELECT COUNT(*) FROM audit_log;" 2>/dev/null || echo "0")
    if [ "$ROW_COUNT" -gt 0 ]; then
        echo "  PASS: audit log has $ROW_COUNT entries"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: audit log is empty"
        FAIL=$((FAIL + 1))
    fi

    # Check that an echo entry exists with correct fields.
    ECHO_ROW=$(sqlite3 "$AUDIT_DB" "SELECT caller, method, tool, upstream, status FROM audit_log WHERE tool='echo' LIMIT 1;" 2>/dev/null || echo "")
    check_contains "audit entry has caller alice" "alice" "$ECHO_ROW"
    check_contains "audit entry has method tools/call" "tools/call" "$ECHO_ROW"
    check_contains "audit entry has tool echo" "echo" "$ECHO_ROW"
    check_contains "audit entry has upstream fake-http" "fake-http" "$ECHO_ROW"
    check_contains "audit entry has status ok" "ok" "$ECHO_ROW"

    # Check params were recorded.
    PARAMS=$(sqlite3 "$AUDIT_DB" "SELECT params FROM audit_log WHERE tool='echo' LIMIT 1;" 2>/dev/null || echo "")
    check_contains "audit entry has params JSON" '"name"' "$PARAMS"

    # Check latency_ms is a positive number.
    LATENCY=$(sqlite3 "$AUDIT_DB" "SELECT latency_ms FROM audit_log WHERE tool='echo' LIMIT 1;" 2>/dev/null || echo "0")
    if [ "$LATENCY" -gt 0 ] 2>/dev/null; then
        echo "  PASS: audit latency_ms is positive ($LATENCY ms)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: audit latency_ms should be positive, got $LATENCY"
        FAIL=$((FAIL + 1))
    fi
fi
echo ""

# --- Summary ---

echo "============================================"
echo "Results: $PASS passed, $FAIL failed"
echo "============================================"

[ "$FAIL" -eq 0 ]
