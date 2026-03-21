# Task 14: SQLite and Rate Limiter Hardening

**Status:** done
**Depends on:** 12
**Needed by:** 15

---

## Goal

Fix resource management issues in the SQLite auth store and rate limiter that could cause failures under load or memory exhaustion.

---

## 1. Add SQLite busy timeout

**File:** `internal/auth/store.go` (after line 65)

WAL mode is enabled but no busy timeout is set. Under concurrent writes, SQLite immediately returns "database is locked" instead of retrying.

**Fix:** Add after the foreign keys pragma:
```go
if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
    db.Close()
    return nil, fmt.Errorf("auth: set busy timeout: %w", err)
}
```

---

## 2. Handle `RowsAffected()` errors

**File:** `internal/auth/store.go:196, 209, 327, 359`

Four locations discard the error from `result.RowsAffected()` with `_`. While unlikely to fail with SQLite, this masks potential driver errors.

**Fix:** Change all four from:
```go
n, _ := result.RowsAffected()
```
to:
```go
n, err := result.RowsAffected()
if err != nil {
    return fmt.Errorf("auth: rows affected: %w", err)
}
```

---

## 3. Cap rate limiter map growth

**File:** `internal/policy/ratelimit.go:221-246`

`toolLimiters` creates a new entry per (caller, tool) pair and never evicts. An attacker with a valid key can exhaust memory by calling many distinct tool names.

**Fix:** Add a maximum size check before creating new entries:
```go
const maxToolLimitersPerCaller = 1000

if len(callerTools) >= maxToolLimitersPerCaller {
    // Return a one-shot limiter instead of caching
    return rate.NewLimiter(r, b)
}
```

---

## 4. Configure database connection pool

**File:** `internal/auth/store.go` (after opening the database)

No connection pool limits are set. Add reasonable defaults:
```go
db.SetMaxOpenConns(5)
db.SetMaxIdleConns(2)
db.SetConnMaxLifetime(30 * time.Minute)
```

---

## Verification

- All existing tests pass
- Add test for busy timeout behavior under concurrent writes
- Verify rate limiter doesn't grow unbounded with many tool names
