# Task 7: Rate Limiting + Policy Enforcement

**Status:** not started
**Depends on:** Task 6 (auth — caller identity needed for per-caller limits)
**Needed by:** Task 8 (observability — rate limit rejections are a metric)

---

## Goal

Add rate limiting, JSON Schema input validation, and global tool blocklist/allowlist. After this task, the gateway prevents runaway agents from overwhelming upstreams and can enforce organization-wide tool policies.

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

## 2. Input Validation

### JSON Schema validation

Optionally validate `tools/call` input params against the tool's JSON Schema before forwarding.

Config:
```yaml
upstreams:
  - name: github
    validate_input: true    # opt-in per upstream
```

When enabled:
1. On `tools/call`, look up the tool's `inputSchema` from the cached schema in the route table
2. Validate the request's `params.arguments` against it
3. If invalid → return JSON-RPC error (code `-32602`, "invalid params") with validation details
4. If valid → proceed with proxying

Use `santhosh-tekuri/jsonschema` for validation (already in the dependency list).

### When to skip

- If the upstream has `validate_input: false` (default), skip validation entirely
- If the tool has no `inputSchema`, skip validation
- Validation should not block requests when the schema is unavailable (e.g., upstream is stale)

---

## 3. Global Tool Blocklist/Allowlist

Beyond per-caller ACLs, support organization-wide tool policies:

```yaml
tool_policy:
  blocklist:
    - "dangerous_tool"
    - "internal/*_admin"
  allowlist: []  # empty = allow everything not blocked
```

Behavior:
- Blocklisted tools are removed from all `tools/list` responses and rejected on `tools/call`, regardless of caller ACLs
- If an allowlist is specified (non-empty), only those tools are available — everything else is blocked
- Blocklist takes priority over allowlist

This is applied as a filter in the proxy handler, after router resolution but before caller ACL checks.

---

## 4. Policy Middleware

Create a policy middleware or checker that the proxy handler calls before forwarding a request:

```go
type PolicyEngine struct {
    rateLimiter *RateLimiter
    validator   *SchemaValidator  // nil if no upstreams have validate_input
    toolPolicy  *ToolPolicy
}

func (p *PolicyEngine) Check(caller, tool, upstream string, params json.RawMessage, schema *transport.ToolSchema) error
```

Returns nil if allowed, or an error that maps to the appropriate JSON-RPC error response.

---

## 5. Testable Deliverables

### Rate limiter tests (`internal/policy/`)

1. **Under limit passes:** 5 requests at 10/sec → all allowed
2. **Over limit rejects:** 20 requests at 10/sec with no delay → some rejected
3. **Per-caller isolation:** caller A is rate limited, caller B is not affected
4. **Per-tool isolation:** caller hits limit on tool A, can still call tool B
5. **Per-upstream limit:** two callers each sending 100/sec to same upstream with 150/sec limit → some rejected
6. **Rate string parsing:** `"100/min"` → rate 1.67/sec, `"10/sec"` → rate 10/sec, `"invalid"` → error

### Input validation tests

7. **Valid input passes:** params matching schema → no error
8. **Invalid input rejected:** params missing required field → error with details
9. **Validation disabled by default:** upstream without validate_input → no validation
10. **Missing schema skips validation:** tool with no inputSchema → passes

### Tool policy tests

11. **Blocklisted tool rejected on call:** → JSON-RPC error
12. **Blocklisted tool hidden from list:** → not in tools/list response
13. **Allowlist restricts to listed tools:** only allowlisted tools visible
14. **Blocklist overrides allowlist:** tool in both → blocked

### Integration

15. **Rate limit through full stack:** hit the gateway endpoint rapidly → get rate limit errors after threshold

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 6. Dependencies

This task adds:
- `golang.org/x/time/rate` for token bucket rate limiting
- `santhosh-tekuri/jsonschema` for JSON Schema validation

---

## 7. Out of Scope

- Distributed rate limiting (Redis-backed — not in v0.1 scope)
- Dynamic rate limit adjustment without config reload
- Per-tool rate limit overrides in caller config (can be added later)
