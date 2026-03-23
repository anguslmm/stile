# Task 13: HTTP Transport Hardening

**Status:** done
**Depends on:** 12
**Needed by:** 15

---

## Goal

Fix reliability and security issues in the HTTP and SSE transport layers that could cause hangs, memory exhaustion, or incorrect health tracking.

---

## 1. Set HTTP response header timeout

**File:** `internal/transport/http.go`

The default `http.Client{}` has no timeout. A hung upstream blocks the goroutine forever.

**Fix:** Set `ResponseHeaderTimeout` on the underlying `http.Transport`, using the per-upstream timeout from config (default 60s):
```go
httpTransport := &http.Transport{
    ResponseHeaderTimeout: timeout,
}
```

**Note:** The original plan was to set `http.Client.Timeout`, but that applies to the entire request lifecycle including body reads. Since Stile proxies SSE streams (which are long-lived responses), a blanket client timeout would kill streaming connections. `ResponseHeaderTimeout` is the correct choice — it catches hung upstreams that never respond, while allowing SSE streams to run indefinitely once headers arrive. Per-upstream timeouts are configured via `timeout` in the upstream config block (Task 20).

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
