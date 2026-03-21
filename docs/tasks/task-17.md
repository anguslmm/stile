# Task 17: Redis-backed Rate Limiting

**Status:** todo
**Depends on:** 14, 16

---

## Goal

Replace the in-memory token bucket rate limiter with a Redis-backed implementation so that rate limits are enforced globally across multiple Stile instances. This is the primary blocker for horizontal scaling.

---

## Problem

The current `RateLimiter` in `internal/policy/ratelimit.go` uses in-memory `rate.Limiter` maps. Each instance tracks its own counters independently. With N instances behind a load balancer, a caller's effective rate limit is N times the configured limit.

---

## 1. Define a RateLimiter interface

Extract the current concrete `RateLimiter` behind an interface so the proxy and server don't care about the backend:

```go
// internal/policy/policy.go
type RateLimiter interface {
    Allow(caller, tool, upstream string, roles []string) *Denial
}
```

Rename the current implementation to `LocalRateLimiter` and add a compile-time check.

---

## 2. Add Redis rate limiter config

Extend the config to support a Redis backend for rate limiting:

```yaml
rate_limits:
  backend: redis          # "local" (default) or "redis"
  redis:
    address: "localhost:6379"
    password: ""
    db: 0
    key_prefix: "stile:"  # namespace keys to avoid collisions
  defaults:
    caller: "100/min"
    tool: "20/sec"
```

When `backend` is `local` or omitted, use the existing in-memory implementation. When `backend` is `redis`, use the new Redis implementation.

---

## 3. Implement Redis rate limiter

Create `internal/policy/redis.go`:

Use the sliding window counter pattern with Redis:

```go
type RedisRateLimiter struct {
    client    *redis.Client
    keyPrefix string
    // ... same config fields as LocalRateLimiter for rates/bursts
}
```

**Algorithm:** Use a Lua script for atomic sliding window rate limiting:

```lua
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])  -- window in seconds
local now = tonumber(ARGV[3])

-- Remove expired entries
redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, now .. ':' .. math.random())
    redis.call('EXPIRE', key, window)
    return 1  -- allowed
end
return 0  -- denied
```

**Key structure:**
- Caller limit: `stile:rl:caller:{callerName}`
- Tool limit: `stile:rl:tool:{callerName}:{toolName}`
- Upstream limit: `stile:rl:upstream:{upstreamName}`

**Three levels** (same as current):
1. Per-caller rate
2. Per-caller-per-tool rate
3. Per-upstream rate (global across all callers)

Each `Allow()` call checks all three atomically. If any denies, the request is rejected.

---

## 4. Add Redis dependency

Add `github.com/redis/go-redis/v9` to `go.mod`.

This is only imported when Redis is configured — consider a build tag or lazy import so that deployments using local rate limiting don't need a Redis connection.

---

## 5. Factory function

```go
// internal/policy/factory.go
func NewRateLimiterFromConfig(cfg *config.Config) (RateLimiter, error) {
    switch cfg.RateLimitBackend() {
    case "redis":
        return NewRedisRateLimiter(cfg)
    default:
        return NewLocalRateLimiter(cfg), nil
    }
}
```

Update `main.go` to use the factory.

---

## 6. Graceful degradation

If Redis is unavailable:
- Log an error at startup and refuse to start (fail-closed) — don't silently fall back to local limiting, as that would silently break the multi-instance guarantee
- If Redis becomes unavailable at runtime, `Allow()` should return a denial (fail-closed) and log the error, rather than allowing unlimited traffic

---

## Verification

- All existing rate limit tests pass with `LocalRateLimiter` (renamed)
- Add unit tests for `RedisRateLimiter` using miniredis (`github.com/alicebob/miniredis/v2`) for an in-process Redis
- Add integration test: two instances sharing Redis enforce a single global limit
- Test fail-closed behavior when Redis is down
- Test config parsing for both backends
