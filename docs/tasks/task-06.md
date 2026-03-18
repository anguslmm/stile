# Task 6: Auth Middleware

**Status:** not started
**Depends on:** Task 3 (server), Task 5 (router — for filtered tools/list)
**Needed by:** Task 7 (rate limiting references caller identity)

---

## Goal

Add inbound authentication (agent → gateway) and per-caller tool access control. After this task, only callers with valid API keys can use the gateway, and each caller only sees and can use the tools they're authorized for.

---

## 1. Config Additions

### Extend `internal/config`

Add caller configuration to the config structs. Follow the same pattern as existing config: unexported fields, getters, validated on `Load`.

```yaml
callers:
  - name: claude-code-dev
    api_key_env: DEV_GATEWAY_KEY
    allowed_tools: ["github/*", "linear/*", "db_query"]

  - name: ops-agent
    api_key_env: OPS_GATEWAY_KEY
    allowed_tools: ["deploy/*", "oncall/*", "datadog/*"]
```

New types:

```go
type CallerConfig struct {
    name         string
    apiKeyEnv    string
    allowedTools []string  // glob patterns
}

// Getters: Name(), APIKeyEnv(), AllowedTools()
```

Add to the top-level config:
```go
type Config struct {
    server    serverConfig
    upstreams []UpstreamConfig
    callers   []CallerConfig   // new
}
```

Validation additions:
- Each caller must have a non-empty name
- Caller names must be unique
- `api_key_env` must be non-empty
- `allowed_tools` must be non-empty (a caller with no allowed tools is useless)

---

## 2. Auth Middleware

### Package: `internal/auth`

### Caller resolution

At startup, load API keys from environment variables and build a lookup table:

1. For each caller in config, read the env var specified by `APIKeyEnv()`
2. If the env var is empty, log a warning (caller is effectively disabled)
3. Hash the key with SHA-256
4. Store in a map: `hash → *Caller`

```go
type Caller struct {
    Name         string
    AllowedTools []string  // glob patterns
}

type Authenticator struct {
    callers map[[32]byte]*Caller  // SHA-256 hash → caller
}
```

### Authentication

```go
func (a *Authenticator) Authenticate(r *http.Request) (*Caller, error)
```

1. If auth is disabled (no callers configured) → return nil, nil
2. Extract `Authorization: Bearer <token>` header
3. Hash the token with SHA-256
4. Look up in the callers map
5. If not found → return error

### Constructor

```go
func NewAuthenticator(callers []config.CallerConfig) (*Authenticator, error)
```

Reads env vars and builds the hash table. If zero callers are configured, returns a valid Authenticator with auth disabled (all requests pass through). If individual env vars are missing, skip that caller with a warning (don't fail entirely). Returns an error only if the configuration is malformed (e.g., a caller has an invalid name).

---

## 3. Tool Access Control

### Glob matching

Use `gobwas/glob` (already in the design doc's dependency list) to match tool names against a caller's `allowed_tools` patterns.

```go
func (c *Caller) CanAccessTool(toolName string) bool
```

Compile the glob patterns once at startup and store them on the `Caller` struct.

### Filtered tools/list

When the gateway handles `tools/list`, it must filter the merged tool list to only include tools the authenticated caller is allowed to access:

1. Get the full tool list from the router
2. Filter by `caller.CanAccessTool(tool.Name)`
3. Return the filtered list

### tools/call authorization

Before proxying a `tools/call`, check that the caller is allowed to access the requested tool. If not, return a JSON-RPC error (code `-32000`, message "access denied").

---

## 4. Middleware Integration

### Where MCP auth plugs in

Auth should be an HTTP middleware that wraps the MCP endpoint handler (`POST /mcp`). It runs before any MCP request dispatch.

```go
func (a *Authenticator) Middleware(next http.Handler) http.Handler
```

The middleware:
1. Calls `Authenticate(r)`
2. If error → write a JSON-RPC error response directly (code `-32000`, "unauthorized") and return
3. If caller is nil (auth disabled) → call `next` with no caller in context
4. If caller is non-nil → store the `*Caller` in the request context, call `next`

The proxy handler retrieves the caller from context to perform tool filtering and access checks. When the caller is nil (auth disabled), the proxy skips tool filtering and allows all tools.

### Admin endpoint auth

Admin endpoints (`POST /admin/refresh`, `POST /admin/reload`) are **outside** the MCP auth middleware. They are protected by a separate, simpler mechanism: a single admin API key.

```go
func AdminAuthMiddleware(adminKeyHash [32]byte, authEnabled bool) func(http.Handler) http.Handler
```

Configuration: the admin key is read from the `ADMIN_API_KEY` environment variable at startup.

Behavior:
1. Extract `Authorization: Bearer <token>` from the request
2. Hash with SHA-256 and compare to the stored admin key hash
3. If valid → call `next`
4. If invalid → return `403 Forbidden` with JSON body `{"error": "forbidden"}`

**Dev mode:** If `ADMIN_API_KEY` is not set **and** no callers are configured (auth is fully disabled), admin endpoints are open — no token required. If `ADMIN_API_KEY` is not set **but** callers are configured (MCP auth is active), admin endpoints return `403 Forbidden` unconditionally. This prevents accidentally exposing admin endpoints in a production config that simply forgot to set the admin key.

### Endpoints outside all auth

The following endpoints are registered without any auth middleware:
- `GET /healthz` — liveness probe (Task 9)
- `GET /readyz` — readiness probe (Task 9)
- `GET /metrics` — Prometheus metrics (Task 8)

These are consumed by infrastructure tooling (Kubernetes, Prometheus) that cannot present bearer tokens.

### HTTP mux topology

The server's mux should look like this (pseudocode):

```
mux.Handle("POST /mcp",        mcpAuth(proxyHandler))
mux.Handle("POST /admin/",     adminAuth(adminHandler))
mux.Handle("GET /healthz",     healthHandler)
mux.Handle("GET /readyz",      readyHandler)
mux.Handle("GET /metrics",     metricsHandler)
```

### Context key

```go
func CallerFromContext(ctx context.Context) *Caller
```

---

## 5. Testable Deliverables

### Config tests

1. **Caller config loads:** YAML with callers section → correct CallerConfig values via getters
2. **Missing caller name:** caller without name → Load returns error
3. **Missing api_key_env:** caller without api_key_env → Load returns error
4. **Missing allowed_tools:** caller with empty allowed_tools → Load returns error
5. **Duplicate caller names:** → Load returns error

### Auth tests (`internal/auth/`)

6. **Valid key authenticates:** set env var, create authenticator, authenticate with correct key → returns caller
7. **Invalid key rejected:** authenticate with wrong key → error
8. **Missing header rejected:** request with no Authorization header → error
9. **Malformed header rejected:** Authorization header without "Bearer " prefix → error
10. **Missing env var skips caller:** env var not set → authenticator created without that caller, other callers still work
11. **Zero callers returns auth-disabled:** no callers in config → NewAuthenticator succeeds, Authenticate returns nil caller with no error

### Access control tests

11. **Exact tool match:** allowed_tools `["db_query"]`, check `"db_query"` → true
12. **Glob match:** allowed_tools `["github/*"]`, check `"github/create_pr"` → true
13. **Glob reject:** allowed_tools `["github/*"]`, check `"linear/list_issues"` → false
14. **Multiple patterns:** allowed_tools `["github/*", "db_query"]` → matches both

### Integration tests

16. **Authenticated tools/list is filtered:** caller with `["github/*"]` → only sees github tools, not others
17. **Unauthorized tools/call rejected:** caller tries to call a tool not in their allowed_tools → JSON-RPC error
18. **No auth config disables auth:** config with no callers section → all requests pass through

### Admin auth tests

19. **Valid admin key accepted:** set `ADMIN_API_KEY`, POST /admin/refresh with correct key → 200
20. **Invalid admin key rejected:** POST /admin/refresh with wrong key → 403
21. **Missing admin key rejected:** POST /admin/refresh with no Authorization header → 403
22. **Admin key unset, auth disabled (dev mode):** no `ADMIN_API_KEY`, no callers configured → admin endpoints return 200
23. **Admin key unset, auth enabled:** no `ADMIN_API_KEY`, callers configured → admin endpoints return 403
24. **Health endpoints skip auth:** GET /healthz and GET /readyz → 200 regardless of auth config

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 6. Dependencies

This task adds: `gobwas/glob` for tool name pattern matching.

---

## 7. Out of Scope

- Rate limiting per caller (Task 7)
- OAuth2/OIDC (explicitly a non-goal for v0.1)
- API key rotation or management UI
