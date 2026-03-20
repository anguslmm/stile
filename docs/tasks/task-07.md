# Task 7: Rate Limiting

**Status:** done
**Depends on:** Task 6 (auth — caller identity needed for per-caller limits)
**Needed by:** Task 8 (observability — rate limit rejections are a metric)

---

## Goal

Add token bucket rate limiting to the gateway. After this task, the gateway prevents runaway agents from overwhelming upstreams and protects shared infrastructure from any single caller monopolizing capacity.

---

## 1. Rate Limiting

### Package: `internal/policy`

### Three granularities

Token bucket rate limiting using `golang.org/x/time/rate`:

1. **Per-caller:** total requests/sec across all tools. Prevents any single agent from monopolizing the gateway.
2. **Per-caller-per-tool:** requests/sec for a specific tool by a specific caller. Prevents one tool from consuming a caller's entire budget.
3. **Per-upstream:** total requests/sec to a given upstream. Protects upstream servers from aggregate load across all callers.

### Config additions

Extend the config to support rate limit declarations:

```yaml
callers:
  - name: claude-code-dev
    api_key_env: DEV_GATEWAY_KEY
    allowed_tools: ["github/*", "linear/*", "db_query"]
    rate_limit: 100/min
    tool_rate_limit: 20/min   # per-tool default for this caller

upstreams:
  - name: github
    url: https://mcp.github.com/sse
    transport: streamable-http
    rate_limit: 200/min       # aggregate across all callers
    auth:
      type: bearer
      token_env: GITHUB_MCP_TOKEN

rate_limits:
  default_caller: 60/min      # applies when caller has no explicit limit
  default_tool: 30/min         # per-caller-per-tool default
  default_upstream: 300/min    # per-upstream default
```

Parse rate limit strings like `"100/min"`, `"10/sec"`, `"5000/hour"` into a rate and burst size. Burst = rate (allow short bursts up to 1 second of capacity).

### Rate limiter implementation

```go
type RateLimiter struct {
    callerLimiters   map[string]*rate.Limiter           // caller name → limiter
    toolLimiters     map[string]map[string]*rate.Limiter // caller name → tool name → limiter
    upstreamLimiters map[string]*rate.Limiter            // upstream name → limiter
    mu               sync.RWMutex                        // for lazy tool limiter creation
}

func NewRateLimiter(cfg *config.Config) *RateLimiter

func (rl *RateLimiter) Allow(caller, tool, upstream string) bool
```

`Allow` checks all three levels. If any returns false, the request is rejected. Tool-level limiters are created lazily on first use (since the set of tool names isn't known until runtime).

### Rejection response

When rate limited, return a JSON-RPC error:
- Code: `-32000`
- Message: `"rate limit exceeded"`
- Data: include which limit was hit (caller, tool, or upstream) for debuggability

---

## 2. Testable Deliverables

### Rate limiter tests (`internal/policy/`)

1. **Under limit passes:** 5 requests at 10/sec → all allowed
2. **Over limit rejects:** 20 requests at 10/sec with no delay → some rejected
3. **Per-caller isolation:** caller A is rate limited, caller B is not affected
4. **Per-tool isolation:** caller hits limit on tool A, can still call tool B
5. **Per-upstream limit:** two callers each sending 100/sec to same upstream with 150/sec limit → some rejected
6. **Rate string parsing:** `"100/min"` → rate 1.67/sec, `"10/sec"` → rate 10/sec, `"invalid"` → error

### Integration

7. **Rate limit through full stack:** hit the gateway endpoint rapidly → get rate limit errors after threshold

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 3. Dependencies

This task adds:
- `golang.org/x/time/rate` for token bucket rate limiting

---

## 4. Out of Scope

- Distributed rate limiting (Redis-backed — not in v0.1 scope)
- Dynamic rate limit adjustment without config reload
- Per-tool rate limit overrides in caller config (can be added later)
- JSON Schema input validation (upstreams validate their own inputs)
- Global tool blocklist/allowlist (per-caller ACLs from task 6.1 cover this)
