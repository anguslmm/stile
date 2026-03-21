# Task 12: Critical Security Fixes

**Status:** todo
**Depends on:** 11
**Needed by:** 13, 14, 15

---

## Goal

Fix the highest-severity security vulnerabilities found during audit. These are issues that could allow key compromise, denial of service, or weak cryptographic output.

---

## 1. Constant-time admin key comparison

**File:** `internal/auth/auth.go:225`

The `!=` operator on `[32]byte` is not constant-time. An attacker can measure response latency to deduce the admin key hash byte-by-byte.

**Fix:** Replace:
```go
if hash != adminKeyHash {
```
with:
```go
if subtle.ConstantTimeCompare(hash[:], adminKeyHash[:]) != 1 {
```

Add `"crypto/subtle"` to imports. Apply the same fix to the `zeroHash` comparison on line 203 if it touches secret material (it doesn't strictly need it since it's a public check, but consistency is good).

---

## 2. Check `rand.Read()` error in key generation

**File:** `internal/auth/store.go:14,374`

`rand.Read` returns an error that is silently discarded. If the system entropy source fails, a weak or zeroed key is generated.

**Fix:** Change the `cryptoRandRead` variable and `GenerateAPIKey` to propagate the error:
```go
var cryptoRandRead = func(b []byte) error {
    _, err := rand.Read(b)
    return err
}

func GenerateAPIKey() (string, error) {
    b := make([]byte, 16)
    if err := cryptoRandRead(b); err != nil {
        return "", fmt.Errorf("auth: generate key: %w", err)
    }
    return "sk-" + hexEncodeToString(b), nil
}
```

Update all callers of `GenerateAPIKey()` to handle the error.

---

## 3. Request body size limit

**File:** `internal/server/server.go:163`

`io.ReadAll(r.Body)` has no size bound. A malicious client can OOM the process.

**Fix:** Replace:
```go
body, err := io.ReadAll(r.Body)
```
with:
```go
const maxRequestBody = 10 << 20 // 10 MB
body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
```

Consider making this configurable via config if needed later.

---

## 4. Response body size limit

**File:** `internal/transport/http.go:89`

Same issue on the upstream side — `io.ReadAll(resp.Body)` is unbounded.

**Fix:** Same pattern:
```go
const maxResponseBody = 10 << 20 // 10 MB
data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
```

---

## 5. Batch request size limit

**File:** `internal/server/server.go:185-196`

JSON-RPC batch requests can contain unlimited items. Add a cap:
```go
const maxBatchSize = 100
if len(requests) > maxBatchSize {
    writeError(w, nil, jsonrpc.CodeInvalidRequest, "batch too large")
    return
}
```

---

## 6. Require `--dev` flag for open admin endpoints

**Files:** `cmd/gateway/main.go`, `internal/auth/auth.go`

Currently, when `ADMIN_API_KEY` is unset and no callers exist, admin endpoints are fully open ("dev mode"). This is dangerous if someone deploys to production without setting the env var — there's a race window where an attacker could create callers before the legitimate operator.

**Fix:**

1. Add a `--dev` flag to the main `flag.Parse()` in `main.go`:
   ```go
   devMode := flag.Bool("dev", false, "enable dev mode (open admin API without ADMIN_API_KEY)")
   ```

2. Change `buildAuthOpts` to accept the `devMode` bool. When `ADMIN_API_KEY` is not set:
   - If `--dev` is passed: allow the current open behavior, but log a loud warning:
     ```go
     slog.Warn("ADMIN_API_KEY not set — admin endpoints are open (dev mode)")
     ```
   - If `--dev` is NOT passed: refuse to start. Print an error:
     ```
     error: ADMIN_API_KEY not set and --dev not specified; refusing to start with open admin endpoints
     ```

3. Update `AdminAuthMiddleware` in `internal/auth/auth.go`: instead of the implicit "no key + no callers = open" logic, accept an explicit `devMode bool` parameter. When `devMode` is false and no admin key is configured, always return 403.

4. Log at startup when dev mode is active:
   ```go
   slog.Warn("running in dev mode — admin endpoints are open without authentication")
   ```

---

## Verification

- All existing tests pass
- Add test for oversized request body (expect error)
- Add test for oversized batch (expect error)
- Add test that `GenerateAPIKey` propagates errors (mock `cryptoRandRead`)
- Verify timing-attack fix with `subtle.ConstantTimeCompare`
- Verify server refuses to start without `ADMIN_API_KEY` or `--dev`
- Verify `--dev` allows open admin and logs warning
- Verify without `--dev`, missing `ADMIN_API_KEY` returns 403 on admin endpoints
