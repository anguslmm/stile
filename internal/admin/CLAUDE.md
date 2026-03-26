# admin

HTTP admin API for managing callers, API keys, and roles at runtime. Includes an embedded web UI dashboard.

## Key Exported Types

- **`Handler`** — HTTP handler that serves all `/admin/...` endpoints including the embedded web UI. Wraps an `auth.Store` and `*router.RouteTable`. Routes must be protected by admin auth middleware by the caller — `Register` itself applies no auth.
- **`Option`** — Functional option for configuring `Handler` with health checker, config, start time, and audit reader.
- **`ConfigView`** — JSON-safe, sanitized representation of the running config (no secrets or TLS key paths).
- **`Client`** — Thin HTTP client that mirrors the same operations as `auth.Store` (plus cache/refresh), targeting a remote Stile instance. Used by the admin CLI's `--remote` mode.

## Key Exported Functions

- **`NewHandler(store auth.Store, rt *router.RouteTable, opts ...Option) *Handler`** — Constructs the handler with optional configuration.
- **`WithHealthChecker(hc *health.Checker) Option`** — Adds upstream health data to the status endpoint.
- **`WithConfig(cfg *config.Config) Option`** — Enables the `/admin/config` endpoint with a sanitized config view.
- **`WithStartTime(t time.Time) Option`** — Sets the gateway start time for uptime reporting.
- **`WithAuditReader(r audit.Reader) Option`** — Enables the `/admin/audit` query endpoint.
- **`WithTokenStore(ts auth.TokenStore) Option`** — Enables the `/admin/connections` endpoints for managing OAuth tokens.
- **`WithOAuthProviders(names []string) Option`** — Sets the configured OAuth provider names for the connections UI.
- **`NewConfigView(cfg *config.Config) ConfigView`** — Produces a sanitized, JSON-safe view of the running config.
- **`(h *Handler) Register(mux *http.ServeMux)`** — Registers all routes (API + UI) on the provided mux.
- **`NewClient(baseURL, adminKey string) *Client`** — Constructs the remote client; sends `Authorization: Bearer <adminKey>` on every request.

## Routes Registered by Handler

### API Endpoints
- `POST/GET /admin/callers` — create, list callers
- `GET/DELETE /admin/callers/{name}` — get, delete caller
- `POST/GET/DELETE /admin/callers/{name}/keys` and `.../keys/{id}` — create, list, delete keys
- `POST/GET/DELETE /admin/callers/{name}/roles` and `.../roles/{role}` — assign, list, remove roles
- `POST /admin/refresh` — trigger route table refresh
- `GET/DELETE /admin/cache` — cache stats / flush (no-ops if store is not `auth.Cacheable`)
- `GET /admin/config` — sanitized running configuration (JSON)
- `GET /admin/status` — upstream health, caller count, uptime (JSON)
- `GET /admin/audit` — audit log query with filters (JSON)
- `GET /admin/connections` — list OAuth connections for a user (`?caller=...`)
- `DELETE /admin/connections/{provider}` — disconnect a user's OAuth token (`?caller=...`)
- `PUT /admin/connections/{provider}` — admin escape hatch: insert/overwrite a token for a user

### Web UI (htmx + Go templates)
- `GET /admin/ui/` — dashboard (upstream health, caller count, uptime)
- `GET /admin/ui/callers` — caller list with add/delete
- `GET /admin/ui/callers/{name}` — caller detail with key and role management
- `POST /admin/ui/callers/{name}/keys` — generate API key (renders detail page with key shown once)
- `POST /admin/ui/callers/{name}/roles` — assign role (redirects to detail)
- `GET /admin/ui/config` — read-only config viewer (formatted JSON)
- `GET /admin/ui/connections` — OAuth connections management (list providers, connect/disconnect per user)
- `GET /admin/ui/audit` — filterable, paginated audit log browser

## Design Notes

- `Handler` type-asserts `auth.Store` to `auth.Cacheable` at request time; cache endpoints gracefully return a status message if the store doesn't implement it.
- Raw API keys are never stored; `Handler.createKey` generates the key, SHA-256 hashes it, and stores only the hash. The raw key is returned once in the response.
- `Client.RevokeKey` resolves label to ID by calling `ListKeys` first — there is no server-side label-based delete endpoint.
- All response types (`callerListItem`, `keyItem`, etc.) are package-private; `Client` maps them to `auth.CallerInfo` / `auth.KeyInfo` for callers.
- Auth middleware is the caller's responsibility — `Register` does not wrap routes.
- The embedded UI uses htmx (loaded from CDN) + Go html/template. Templates are embedded via `embed.FS`. No build step required.
- Config viewer only shows sanitized data — DSNs, passwords, TLS key paths are stripped. Env var names for credentials are shown.
- The UI is automatically gated behind the same admin key auth that protects `/admin/*` routes.
- Connection endpoints gracefully return "OAuth not configured" when no `TokenStore` is wired. The `PUT` endpoint is an admin escape hatch for scripted testing / recovery — it accepts a raw access token and stores it directly, bypassing the OAuth flow.
