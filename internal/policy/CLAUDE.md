# policy

Enforces rate limits at three granularities: per-caller, per-caller-per-tool, and per-upstream.

## Key Types

- **`RateLimiter`** — interface with a single method `Allow(caller, tool, upstream string, roles []string) *RateLimitResult`. Both implementations must be safe for concurrent use.
- **`LocalRateLimiter`** — in-memory token bucket implementation via `golang.org/x/time/rate`. Per-upstream limiters are created eagerly; per-caller and per-tool limiters are created lazily on first `Allow` call.
- **`RedisRateLimiter`** — distributed sliding window implementation backed by Redis. Uses a Lua script (`ZSET` of microsecond timestamps) for atomic checks. Fails closed if Redis is unreachable.
- **`RateLimitResult`** — returned by `Allow`; carries `Denial` (nil if allowed), `Limit`, `Remaining`, `ResetAt`, and `RetryAfter` for use in response headers.
- **`Denial`** — identifies which level denied the request (`"caller"`, `"tool"`, or `"upstream"`).

## Key Functions

- **`NewLocalRateLimiter(cfg)`** — constructs an in-memory limiter from config.
- **`NewRedisRateLimiter(cfg)`** — constructs a Redis-backed limiter; pings Redis at startup and returns an error if unreachable.
- **`NewRateLimiterFromConfig(cfg)`** — factory that dispatches to local or Redis based on `cfg.RateLimitBackend()`.
- **`CloseRateLimiter(rl)`** — type-asserts to `*RedisRateLimiter` and calls `Close()`; no-op for local.

## Design Decisions

- **`Allow` returns nil** when no limits are configured at any level — callers must nil-check before inspecting the result.
- **Role resolution picks the most permissive limit** across a caller's roles (highest rate wins). Role rates are resolved and cached on the first `Allow` call for a given caller; subsequent calls for that caller ignore role changes.
- **`LocalRateLimiter` caps tool limiters at 1000 per caller** (`maxToolLimitersPerCaller`). Beyond that, a transient (uncached) limiter is returned to prevent unbounded memory growth.
- **`RedisRateLimiter` is fail-closed**: any Redis error during a check returns a denial rather than allowing the request through.
- **`ResetAt` semantics differ** between backends: local uses token-refill time, Redis uses `now + window`.
