# Task 24: Rate Limit Response Headers

**Status:** todo
**Depends on:** 15

---

## Goal

Return standard rate limit headers on every response so clients can see their remaining budget and back off gracefully when approaching limits, instead of being surprised by a hard denial.

---

## 1. Add rate limit headers to all responses

After every rate limit check (whether allowed or denied), include headers in the HTTP response:

```
X-RateLimit-Limit: 100          # requests allowed in the window
X-RateLimit-Remaining: 73       # requests remaining in the current window
X-RateLimit-Reset: 1711036800   # Unix timestamp when the window resets
```

On denial, also include:

```
Retry-After: 12                 # seconds until the client should retry
```

**Implementation:**

- Extend `RateLimiter.Allow()` (or add a new method) to return rate limit state alongside the allow/deny decision:
  ```go
  type RateLimitResult struct {
      Allowed   bool
      Limit     int
      Remaining int
      ResetAt   time.Time
      // On denial:
      RetryAfter time.Duration
      Denial     *Denial  // nil if allowed
  }
  ```
- In `proxy.HandleToolsCall`, after the rate limit check, set the headers on the `http.ResponseWriter` before writing the response body
- For the local (in-memory) rate limiter, derive remaining/reset from the `rate.Limiter` token count and rate
- For the Redis rate limiter (task 18), the sliding window Lua script can return the count and TTL alongside the allow/deny decision

---

## 2. Which limit to report

A request is checked against up to three limits (per-caller, per-tool, per-upstream). Report the **most restrictive** — the one with the lowest `Remaining` count. This gives clients the most useful signal.

If multiple limits deny the request, report the one that denied first (same as the current `Denial.Level` field).

---

## 3. Headers on non-tool-call requests

Rate limits currently only apply to `tools/call`. For `tools/list`, `initialize`, and `ping`, don't include rate limit headers (there's no limit to report).

---

## Verification

- Add test: allowed request includes `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` headers
- Add test: denied request includes `Retry-After` header
- Add test: `Remaining` decreases with each request
- Add test: headers reflect the most restrictive of the applicable limits
- Add test: non-tool-call requests do not include rate limit headers
