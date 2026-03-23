# Task 6: Auth Middleware

**Status:** done
**Depends on:** Task 3 (server), Task 5 (router — for filtered tools/list)
**Needed by:** Task 7 (rate limiting references caller identity)

---

## Goal

Add inbound authentication (agent → gateway), per-caller tool access control, and per-caller outbound credential injection. After this task, callers authenticate with API keys, each key maps to an auth env that determines which upstream credentials are used, and each caller only sees and can use the tools they're authorized for.

---

## 1. Auth Envs (Config)

### Extend `internal/config`

Auth envs are named bundles of upstream credentials, defined in YAML. Each auth env maps upstream names to environment variable names containing bearer tokens.

```yaml
auth_envs:
  dev:
    github: GITHUB_DEV_TOKEN
    notion: NOTION_DEV_TOKEN
  prod:
    github: GITHUB_PROD_TOKEN
    notion: NOTION_PROD_TOKEN
```

New config type:

```go
type AuthEnvConfig struct {
    name        string
    credentials map[string]string  // upstream name → env var name
}

// Getters: Name(), Credentials()
```

Add to the top-level config:
```go
type Config struct {
    server    serverConfig
    upstreams []UpstreamConfig
    authEnvs  []AuthEnvConfig   // new
}
```

Validation:
- Auth env names must be non-empty and unique
- Each credential must reference a valid upstream name (from the upstreams list)
- Env var names must be non-empty

At startup, resolve env vars to actual token values and store in memory. Log a warning for missing env vars (the auth env is partially usable but those upstreams won't have credentials).

---

## 2. Caller Database

### Package: `internal/auth`

Caller data lives in a SQLite database rather than config. This supports dynamic user management and allows callers to have multiple API keys mapping to different auth envs.

### Schema

```sql
CREATE TABLE callers (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    allowed_tools TEXT NOT NULL,  -- JSON array of glob patterns, e.g. '["github/*","notion/*"]'
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE api_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    caller_id   INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    key_hash    BLOB NOT NULL UNIQUE,    -- SHA-256 hash of the API key
    auth_env    TEXT NOT NULL,            -- name of the auth env this key grants
    label       TEXT,                     -- optional human-readable label
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);
```

### CallerStore interface

```go
type Caller struct {
    Name         string
    Roles        []string    // all roles assigned to this caller, in config order
    AllowedTools []glob.Glob // union of all roles' patterns
}

type KeyLookupResult struct {
    Caller   *Caller
    KeyLabel string
}

type CallerStore interface {
    LookupByKey(hashedKey [32]byte) (*KeyLookupResult, error)
    RolesForCaller(name string) ([]string, error)
}
```

`LookupByKey` returns a `KeyLookupResult` carrying both the `Caller` and the key's label (used for audit logging and metrics). `RolesForCaller` returns the roles assigned to a caller via the `caller_roles` table (see Task 6.2).

**Note:** The original design used per-key `AuthEnv` strings. Task 6.1 replaced auth envs with roles, and Task 6.2 decoupled roles from keys entirely — roles are now assigned to callers, not individual keys.

### SQLite implementation

```go
type SQLiteStore struct {
    db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error)
func (s *SQLiteStore) LookupByKey(hashedKey [32]byte) (*Caller, error)
func (s *SQLiteStore) Close() error
```

The constructor opens (or creates) the database, runs migrations, and returns the store. Use `modernc.org/sqlite` (pure Go, no CGo) to keep the single-binary deployment story.

### Seeding callers

For v0.1, provide a CLI subcommand or a seed function for creating callers and API keys:

```bash
stile add-caller --name angus --allowed-tools 'github/*,notion/*'
stile add-key --caller angus --auth-env dev
# prints: sk-<random>  (the only time the raw key is shown)
```

This is intentionally minimal — no full admin API yet.

---

## 3. Auth Middleware

### Authenticator

```go
type Authenticator struct {
    store    CallerStore
    authEnvs map[string]map[string]string  // env name → upstream name → token value
}

func NewAuthenticator(store CallerStore, envs []config.AuthEnvConfig) (*Authenticator, error)
```

The constructor resolves auth env config into actual token values (reading env vars) and stores them.

### Authentication

```go
func (a *Authenticator) Authenticate(r *http.Request) (*Caller, string, error)
```

Returns the authenticated caller, the label of the API key used (for audit/metrics), and any error.

1. Extract `Authorization: Bearer <token>` header
2. Hash the token with SHA-256
3. Look up via `store.LookupByKey(hash)`
4. If not found → return error
5. Resolve caller's roles via `store.RolesForCaller(name)` and build the `Caller` with ordered roles and union of tool globs

### Credential injection

```go
func (a *Authenticator) UpstreamToken(roles []string, upstreamName string) (string, bool)
```

Returns the bearer token for a given upstream, checking the caller's roles in config order. The proxy calls this when forwarding a request to inject the correct outbound credential.

**Note:** The original signature took a single `authEnv` string. Task 6.1 replaced auth envs with roles, and Task 6.2 changed this to accept a `[]string` of roles.

---

## 4. Tool Access Control

### Glob matching

Use `gobwas/glob` to match tool names against a caller's `allowed_tools` patterns.

```go
func (c *Caller) CanAccessTool(toolName string) bool
```

Compile the glob patterns when constructing the `Caller` (in `LookupByKey`).

### Filtered tools/list

When the gateway handles `tools/list`, filter the merged tool list to only include tools the authenticated caller is allowed to access:

1. Get the full tool list from the router
2. Filter by `caller.CanAccessTool(tool.Name)`
3. Return the filtered list

### tools/call authorization

Before proxying a `tools/call`, check that the caller is allowed to access the requested tool. If not, return a JSON-RPC error (code `-32000`, message "access denied").

---

## 5. Middleware Integration

### HTTP middleware

```go
func (a *Authenticator) Middleware(next http.Handler) http.Handler
```

The middleware:
1. Calls `Authenticate(r)`
2. If error → write a JSON-RPC error response (code `-32000`, "unauthorized") and return
3. If caller is nil (auth disabled) → call `next` with no caller in context
4. If caller is non-nil → store the `*Caller` in the request context, call `next`

The proxy handler retrieves the caller from context to perform tool filtering, access checks, and credential injection. When the caller is nil (auth disabled), the proxy skips tool filtering, allows all tools, and uses the upstream's default credentials (from the upstream config `auth` block, for backwards compatibility).

### Admin endpoint auth

Admin endpoints (`POST /admin/refresh`, `POST /admin/reload`) are **outside** the MCP auth middleware. Protected by a single admin API key.

```go
func AdminAuthMiddleware(adminKeyHash [32]byte, devMode bool, opts ...AdminAuthOption) func(http.Handler) http.Handler
```

Configuration: the admin key is read from the `ADMIN_API_KEY` environment variable at startup. The `--dev` flag controls dev mode.

Options:
- `WithSessionCheck(fn func(*http.Request) bool)` — adds an alternative auth check (e.g. session cookies for the web UI, Task 30). If the Bearer token is missing or invalid but `sessionCheck` returns true, the request is allowed through.

Behavior:
1. Login/logout UI routes (`/admin/ui/login`, `/admin/ui/logout`) are always exempt
2. If no admin key is configured and `devMode` is true → allow through
3. If no admin key is configured and `devMode` is false → return `403 Forbidden`
4. Extract `Authorization: Bearer <token>` from the request
5. Hash with SHA-256 and compare (constant-time) to the stored admin key hash
6. If valid → call `next`
7. If invalid but `sessionCheck` returns true → call `next`
8. Otherwise → return `403 Forbidden` with JSON body `{"error": "forbidden"}`

### Endpoints outside all auth

Registered without any auth middleware:
- `GET /healthz` — liveness probe (Task 9)
- `GET /readyz` — readiness probe (Task 9)
- `GET /metrics` — Prometheus metrics (Task 8)

### HTTP mux topology

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

## 6. Config Changes Summary

The upstream-level `auth` block (from task 2) remains as a **default** for when auth is disabled or no auth env provides credentials for that upstream. When a caller authenticates with an API key mapped to an auth env, the auth env credentials take precedence.

```yaml
server:
  address: ":8080"
  db_path: "/data/stile.db"   # new — path to SQLite database

auth_envs:                     # new
  dev:
    github: GITHUB_DEV_TOKEN
    notion: NOTION_DEV_TOKEN
  prod:
    github: GITHUB_PROD_TOKEN

upstreams:
  - name: github
    url: https://mcp.github.com/sse
    transport: streamable-http
    auth:                       # default/fallback credentials
      type: bearer
      token_env: GITHUB_DEFAULT_TOKEN
```

---

## 7. Testable Deliverables

### Config tests

1. **Auth env config loads:** YAML with auth_envs section → correct AuthEnvConfig values
2. **Invalid auth env (empty name):** → Load returns error
3. **Invalid auth env (unknown upstream):** references upstream not in upstreams list → Load returns error
4. **db_path loads:** server config includes db_path → accessible via getter

### CallerStore tests (`internal/auth/`)

5. **Create and lookup caller:** insert caller + key → LookupByKey returns correct caller with auth env
6. **Unknown key returns error:** lookup with unknown hash → error
7. **Multiple keys same caller:** caller with keys for "dev" and "prod" → each key resolves to the correct auth env
8. **Multiple callers:** two callers with different keys → each resolves correctly
9. **Deleted caller:** remove caller → their keys no longer resolve

### Auth middleware tests

10. **Valid key authenticates:** request with valid bearer token → caller in context
11. **Invalid key rejected:** request with unknown token → JSON-RPC error
12. **Missing header rejected:** request with no Authorization header → error
13. **Malformed header rejected:** Authorization header without "Bearer " prefix → error
14. **Auth disabled (no callers):** empty database, no auth envs → requests pass through

### Credential injection tests

15. **UpstreamToken resolves:** auth env "dev" + upstream "github" → returns correct token
16. **UpstreamToken missing upstream:** auth env "dev" + upstream "datadog" (not in dev) → returns false
17. **UpstreamToken unknown env:** auth env "staging" (doesn't exist) → returns false

### Access control tests

18. **Exact tool match:** allowed_tools `["db_query"]`, check `"db_query"` → true
19. **Glob match:** allowed_tools `["github/*"]`, check `"github/create_pr"` → true
20. **Glob reject:** allowed_tools `["github/*"]`, check `"linear/list_issues"` → false
21. **Multiple patterns:** allowed_tools `["github/*", "db_query"]` → matches both

### Integration tests

22. **Authenticated tools/list is filtered:** caller with `["github/*"]` → only sees github tools
23. **Unauthorized tools/call rejected:** caller tries to call tool not in allowed_tools → JSON-RPC error
24. **Correct upstream credentials injected:** caller with "dev" key → upstream receives dev token
25. **Auth disabled passes all:** no callers in DB → all requests pass through, default upstream auth used

### Admin auth tests

26. **Valid admin key accepted:** set `ADMIN_API_KEY`, POST /admin/refresh → 200
27. **Invalid admin key rejected:** wrong key → 403
28. **Admin key unset, no callers (dev mode):** → admin endpoints return 200
29. **Admin key unset, callers exist:** → admin endpoints return 403
30. **Health endpoints skip auth:** GET /healthz → 200 regardless of auth config

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 8. Dependencies

This task adds:
- `gobwas/glob` for tool name pattern matching
- `modernc.org/sqlite` for the caller database (pure Go, no CGo)

---

## 9. Out of Scope

- Rate limiting per caller (Task 7)
- OAuth2/OIDC for inbound or outbound auth
- Full admin API for caller management (v0.1 uses CLI seeding)
- API key rotation
