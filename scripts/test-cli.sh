#!/usr/bin/env bash
# test-cli.sh — Integration test for CLI caller management subcommands.
#
# Tests all subcommands: add-caller, add-key, assign-role, unassign-role,
# list-callers, remove-caller, revoke-key.
#
# Usage: ./scripts/test-cli.sh
#
# Builds the binary, runs tests against a temp database, then cleans up.
# Exit code 0 = all pass, 1 = failures.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DB="/tmp/stile-cli-test-$$.db"
STILE=""
PASS=0
FAIL=0

cleanup() {
    rm -f "$DB" "$DB-shm" "$DB-wal"
    [ -n "$STILE" ] && rm -f "$STILE"
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

extract_key() { grep '^  sk-' | tr -d ' '; }

# --- Build ---

echo "Building..."
STILE=$(mktemp)
go build -o "$STILE" ./cmd/gateway/ || exit 1
echo ""

# --- Tests ---

echo "=== add-caller ==="

OUT=$($STILE add-caller --name angus --db "$DB" 2>&1)
check_exit_code "add-caller succeeds" 0 $?
check_contains "prints confirmation" '"angus"' "$OUT"

OUT=$($STILE add-caller --name angus --db "$DB" 2>&1)
check_exit_code "add-caller duplicate fails" 1 $?
check_contains "duplicate error message" "error" "$OUT"

OUT=$($STILE add-caller --name bob --db "$DB" 2>&1)
check_exit_code "add second caller" 0 $?

OUT=$($STILE add-caller --db "$DB" 2>&1)
check_exit_code "add-caller no name fails" 1 $?
echo ""

echo "=== add-key ==="

OUT=$($STILE add-key --caller angus --label "laptop" --db "$DB" 2>&1)
check_exit_code "add-key succeeds" 0 $?
KEY1=$(echo "$OUT" | extract_key)
check_contains "key has sk- prefix" "sk-" "$KEY1"
check_contains "prints security warning" "Store this key securely" "$OUT"

OUT=$($STILE add-key --caller angus --label "desktop" --db "$DB" 2>&1)
check_exit_code "add second key" 0 $?
KEY2=$(echo "$OUT" | extract_key)

# Keys should be different.
if [ "$KEY1" != "$KEY2" ]; then
    echo "  PASS: two keys are different"
    PASS=$((PASS + 1))
else
    echo "  FAIL: two keys are different"
    echo "    both are: $KEY1"
    FAIL=$((FAIL + 1))
fi

OUT=$($STILE add-key --caller nobody --label "x" --db "$DB" 2>&1)
check_exit_code "add-key unknown caller fails" 1 $?
check_contains "unknown caller error" "error" "$OUT"
echo ""

echo "=== assign-role ==="

OUT=$($STILE assign-role --caller angus --role web-tools --db "$DB" 2>&1)
check_exit_code "assign-role succeeds" 0 $?
check_contains "prints confirmation" "web-tools" "$OUT"

OUT=$($STILE assign-role --caller angus --role database --db "$DB" 2>&1)
check_exit_code "assign second role" 0 $?

OUT=$($STILE assign-role --caller bob --role full-access --db "$DB" 2>&1)
check_exit_code "assign role to bob" 0 $?

# Idempotent: assign same role again should succeed.
OUT=$($STILE assign-role --caller angus --role web-tools --db "$DB" 2>&1)
check_exit_code "assign-role idempotent" 0 $?
echo ""

echo "=== list-callers ==="

OUT=$($STILE list-callers --db "$DB" 2>&1)
check_exit_code "list-callers succeeds" 0 $?
check_contains "shows angus" "angus" "$OUT"
check_contains "shows bob" "bob" "$OUT"
check_contains "shows angus key count" "2" "$OUT"
check_contains "shows web-tools role" "web-tools" "$OUT"
check_contains "shows database role" "database" "$OUT"
check_contains "shows full-access role" "full-access" "$OUT"
check_contains "has header" "NAME" "$OUT"
echo ""

echo "=== unassign-role ==="

OUT=$($STILE unassign-role --caller angus --role database --db "$DB" 2>&1)
check_exit_code "unassign-role succeeds" 0 $?

OUT=$($STILE list-callers --db "$DB" 2>&1)
check_not_contains "database role removed" "database" "$OUT"
check_contains "web-tools still present" "web-tools" "$OUT"

OUT=$($STILE unassign-role --caller angus --role nonexistent --db "$DB" 2>&1)
check_exit_code "unassign nonexistent role fails" 1 $?
echo ""

echo "=== revoke-key ==="

OUT=$($STILE revoke-key --caller angus --label laptop --db "$DB" 2>&1)
check_exit_code "revoke-key succeeds" 0 $?
check_contains "prints confirmation" "laptop" "$OUT"

# List keys with no --label to see what remains.
OUT=$($STILE revoke-key --caller angus --db "$DB" 2>&1)
check_contains "remaining key shown" "desktop" "$OUT"
check_not_contains "revoked key gone" "laptop" "$OUT"

OUT=$($STILE revoke-key --caller angus --label bogus --db "$DB" 2>&1)
check_exit_code "revoke nonexistent key fails" 1 $?
echo ""

echo "=== remove-caller ==="

# bob has no keys, should remove without --force.
OUT=$($STILE remove-caller --name bob --db "$DB" 2>&1)
check_exit_code "remove caller with no keys" 0 $?

OUT=$($STILE list-callers --db "$DB" 2>&1)
check_not_contains "bob is gone" "bob" "$OUT"

# angus has 1 key remaining, should require --force.
OUT=$($STILE remove-caller --name angus --db "$DB" 2>&1)
check_exit_code "remove caller with keys needs --force" 1 $?
check_contains "safety message" "--force" "$OUT"

OUT=$($STILE remove-caller --name angus --force --db "$DB" 2>&1)
check_exit_code "remove caller with --force" 0 $?

OUT=$($STILE list-callers --db "$DB" 2>&1)
check_contains "no callers left" "No callers found" "$OUT"
echo ""

echo "=== empty database edge cases ==="

# Fresh DB: list-callers on empty DB.
EMPTY_DB="/tmp/stile-cli-empty-$$.db"
OUT=$($STILE list-callers --db "$EMPTY_DB" 2>&1)
check_exit_code "list-callers on empty DB" 0 $?
check_contains "empty message" "No callers found" "$OUT"
rm -f "$EMPTY_DB" "$EMPTY_DB-shm" "$EMPTY_DB-wal"
echo ""

# --- Summary ---

echo "============================================"
echo "Results: $PASS passed, $FAIL failed"
echo "============================================"

[ "$FAIL" -eq 0 ]
