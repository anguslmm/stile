# Task 13: HTTP Transport Hardening

**Status:** todo
**Depends on:** 12
**Needed by:** 15

---

## Goal

Fix reliability and security issues in the HTTP and SSE transport layers that could cause hangs, memory exhaustion, or incorrect health tracking.

---

## 1. Set HTTP client timeout

**File:** `internal/transport/http.go:36`

The default `http.Client{}` has no timeout. A hung upstream blocks the goroutine forever.

**Fix:**
```go
client: &http.Client{
    Timeout: 60 * time.Second,
},
```

Consider making this configurable per-upstream via config.

---

## 2. Set SSE scanner buffer limit

**File:** `internal/transport/sse.go:31`

`bufio.Scanner` defaults to 64KB max line and panics if exceeded. A malicious upstream can exploit this.

**Fix:**
```go
s := bufio.NewScanner(r)
s.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1 MB max line
return &SSEReader{scanner: s}
```

---

## 3. Distinguish 4xx from 5xx in health tracking

**File:** `internal/transport/http.go:73-77`

Currently any non-2xx response marks the upstream unhealthy. A 400 Bad Request is a client error and should not affect upstream health.

**Fix:** Only call `recordFailure()` for 5xx status codes:
```go
if resp.StatusCode >= 500 {
    t.recordFailure()
} else {
    t.recordSuccess()
}
```

Keep the error return for all non-2xx responses — just change the health tracking.

---

## Verification

- Existing transport tests pass
- Add test: HTTP client respects timeout (mock server that hangs)
- Add test: SSE scanner handles oversized lines gracefully (no panic)
- Add test: 400 response does not mark upstream unhealthy
