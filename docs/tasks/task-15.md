# Task 15: Code Health and Minor Fixes

**Status:** todo
**Depends on:** 12, 13, 14

---

## Goal

Address remaining code health issues, defensive programming gaps, and minor bugs found during audit. None of these are critical on their own, but they improve correctness and maintainability.

---

## 1. StdioTransport: populate `env` field from config

**File:** `internal/transport/stdio.go:52-57`

The `env` field exists on the struct but `NewStdioTransport` never sets it. Environment variables configured for stdio upstreams are silently ignored.

**Fix:** Set `env` from config if the config supports it. If config doesn't have env support yet, either remove the dead field or add config support.

---

## 2. StdioTransport: call `ResetBackoff()` on success

**File:** `internal/transport/stdio.go`

The backoff counter accumulates across successful requests and is never reset. After a successful RoundTrip, call `ResetBackoff()` so the counter doesn't permanently drift toward the max.

---

## 3. Use typed errors instead of string matching in admin handler

**File:** `internal/admin/handler.go:239, 357`

Replace `strings.Contains(err.Error(), "not found")` and `strings.Contains(err.Error(), "UNIQUE constraint failed")` with sentinel errors or typed error checks.

Define sentinel errors in the store package:
```go
var (
    ErrNotFound  = errors.New("auth: not found")
    ErrDuplicate = errors.New("auth: duplicate")
)
```

Then use `errors.Is()` in the admin handler.

---

## 4. Log or return router startup refresh errors

**File:** `internal/router/router.go:85`

`rt.Refresh(context.Background())` silently discards errors. If all upstreams are down at startup, the route table is empty with no indication why.

**Fix:** Log the error at warn level:
```go
if err := rt.Refresh(context.Background()); err != nil {
    slog.Warn("initial route refresh failed", "error", err)
}
```

---

## 5. Admin handler: validate caller exists in `listKeys`

**File:** `internal/admin/handler.go:185-191`

`listKeys` returns an empty list for non-existent callers instead of 404. Other endpoints validate existence first. Make this consistent.

---

## 6. Auth middleware: set HTTP status on JSON-RPC auth errors

**File:** `internal/auth/auth.go:234-236`

`writeJSONRPCError` sends auth failures with HTTP 200. Add `w.WriteHeader(http.StatusUnauthorized)` before the body write so security tooling can detect auth failures.

---

## 7. Handle `glob.Compile` error defensively

**File:** `internal/auth/auth.go:90`

`g, _ := glob.Compile(pattern)` discards the error with a comment that patterns are validated at config time. If a pattern somehow gets through, this will produce a nil glob that panics on match.

**Fix:** Log and skip, or return an error from `NewAuthenticator`:
```go
g, err := glob.Compile(pattern)
if err != nil {
    slog.Error("invalid glob pattern", "pattern", pattern, "error", err)
    continue
}
```

---

## Verification

- All existing tests pass
- Verify stdio env is populated (or field removed)
- Verify admin handler returns 404 for non-existent caller in listKeys
- Verify auth errors return HTTP 401
