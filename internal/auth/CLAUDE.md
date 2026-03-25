# auth

Inbound authentication (API key + OIDC), role-based tool ACLs, outbound credential injection, per-user OAuth token management, and caller/key/role management backed by SQLite or Postgres.

## Key Types

- **`Caller`** — Authenticated identity with name, roles, and compiled glob patterns for tool access. `CanAccessTool(name)` checks ACL.
- **`CallerStore`** (interface) — Read-only hot path: `LookupByKey([32]byte)` and `RolesForCaller(name)`. Used by `Authenticator`.
- **`Store`** (interface) — Full management interface extending `CallerStore`: add/delete callers and keys, assign/unassign roles, list/revoke, `EnsureCaller` (upsert for OIDC auto-provisioning), `CallerExists`.
- **`Authenticator`** — Validates inbound Bearer tokens via OIDC (JWT or userinfo) or API key (SHA-256 hash lookup). Resolves roles, computes union of tool globs, and resolves outbound upstream credentials from env vars. Accepts `AuthenticatorOption` functional options (e.g. `WithOIDCValidator`).
- **`OIDCValidator`** — Validates OIDC tokens in two modes: JWT (local signature verification against JWKS) or userinfo (HTTP call to provider's userinfo endpoint with per-token caching). Created via `NewOIDCValidator(ctx, cfg, opts...)`. Uses `coreos/go-oidc/v3` for discovery and JWT verification.
- **`SQLiteStore`** — `Store` implementation backed by SQLite (via `modernc.org/sqlite`).
- **`PostgresStore`** — `Store` implementation backed by Postgres (via `pgx/v5/stdlib`).
- **`CachedStore`** — Write-through cache wrapping any `Store`. Caches `LookupByKey` and `RolesForCaller` with TTL. Evicts on writes via reverse index (callerName → key hashes).
- **`PGNotifyListener`** — Listens on Postgres channel `stile_auth_invalidate` and evicts `CachedStore` entries. Enables cache coherence across multiple Stile instances.
- **`CacheStats`** / **`Cacheable`** — Observability interface and stats struct for cache hit/miss/eviction counters.
- **`TokenStore`** (interface) — Per-user OAuth token CRUD: `StoreToken`, `GetToken`, `DeleteToken`, `ListProviders`. Backed by `SQLiteTokenStore` or `PostgresTokenStore`.
- **`OAuthToken`** — Holds access_token, refresh_token, token_type, expiry, scopes. `Expired()` checks with a 30-second buffer.
- **`TokenRefresher`** — Exchanges refresh tokens for new access tokens via the provider's token endpoint. Preserves old refresh token if provider doesn't issue a new one.
- **`OAuthResolver`** — Implements `proxy.UpstreamAuthResolver`. Maps upstream names to OAuth providers, looks up per-user tokens, and auto-refreshes expired tokens.
- **`OAuthHandler`** — HTTP handler for the OAuth authorization code flow: `GET /oauth/connect/{provider}` (starts flow with PKCE) and `GET /oauth/callback` (exchanges code for tokens). Uses in-memory state map for CSRF protection.

## Key Functions

- `OpenStore(cfg)` — Factory: returns `SQLiteStore` or `PostgresStore` based on driver config.
- `NewCachedStore(inner, ttl)` — Wraps a store with caching; returns `inner` directly if `ttl == 0`.
- `NewAuthenticator(store, roles, opts...)` — Compiles role configs: reads credential env vars, compiles tool glob patterns. Optional `WithOIDCValidator(v, cfg)` enables OIDC auth.
- `NewOIDCValidator(ctx, cfg, opts...)` — Discovers OIDC provider, sets up JWT verifier or userinfo client. Optional `WithOIDCHTTPClient` for custom HTTP client (testing).
- `GenerateAPIKey()` — Returns a `"sk-"` prefixed 32-hex-char random key.
- `CallerFromContext(ctx)` / `ContextWithCaller(ctx, c)` — Context helpers for propagating `Caller` through the request pipeline.
- `AuthMethodFromContext(ctx)` / `ContextWithAuthMethod(ctx, m)` — Context helpers for the auth method used ("apikey" or "oidc").
- `AdminAuthMiddleware(keyHash, devMode)` — Separate middleware for admin endpoints; uses `crypto/subtle` constant-time compare.
- `OpenTokenStore(cfg)` — Factory for token stores: returns `SQLiteTokenStore` or `PostgresTokenStore`.
- `NewTokenRefresher(providers, client)` — Creates a refresher from config; resolves client_id/secret from env vars at construction.
- `NewOAuthResolver(upstreams, store, refresher)` — Creates a resolver that maps OAuth-authed upstreams to providers and performs per-user token lookup + refresh.
- `NewOAuthHandler(providers, store, baseURL)` — Creates OAuth flow endpoints. `Register(mux)` adds routes.
- `UpstreamTokenFromContext(ctx)` / `ContextWithUpstreamToken(ctx, token)` — Context helpers for passing per-request OAuth tokens to the transport layer.

## OIDC Authentication

The authenticator supports two OIDC token validation modes:

- **JWT mode** (`validation: jwt`): Validates JWT access tokens locally by verifying the signature against the provider's JWKS, checking `iss`, `aud`, and `exp` claims. Used for providers that issue JWT access tokens (Keycloak, Auth0, Okta). Only attempted when the token looks like a JWT (three dot-separated segments).
- **Userinfo mode** (`validation: userinfo`): Validates opaque access tokens by calling the provider's userinfo endpoint. Results are cached per-token with a 5-minute TTL. Used for providers that issue opaque tokens (Google). Skipped for tokens with `sk-` prefix (API keys).

Both modes produce the same `Caller` struct. The authenticator tries OIDC first, then falls back to API key hash lookup.

**Auto-provisioning**: When `auto_provision: true`, callers are created on first OIDC login with `default_roles` using `EnsureCaller` (upsert semantics: `INSERT ... ON CONFLICT DO NOTHING`). Safe for concurrent access across multiple instances. When `auto_provision: false`, the caller must already exist in the DB.

**Domain filtering**: If `allowed_domains` is set, the `email` claim must match one of the listed domains.

## Design Notes

- API keys are never stored in plaintext — only SHA-256 hashes. `LookupByKey` takes `[32]byte`.
- Roles are stored in the DB; glob patterns and outbound credentials are in config/env. The `Authenticator` joins them at auth time.
- Role ordering in `Caller.Roles` follows config order (not DB order). `UpstreamToken` picks the first matching role.
- `CachedStore` evicts by caller name using a reverse index; it never updates cache entries in place.
- SQLite uses WAL mode + foreign keys + busy timeout set via DSN pragma. Includes a destructive migration for pre-6.2 schemas missing `caller_roles`.
- Postgres migrations use `pg_advisory_lock(42)` to prevent concurrent `CREATE TABLE` races on multi-instance startup.
- `PGNotifyListener` uses a dedicated `pgx` connection for `LISTEN` (separate from the `sql.DB` pool) and reconnects with 1s backoff.
- `EnsureCaller` checks `RowsAffected` to only assign default roles on creation (not on subsequent no-op inserts).
- Token store uses `pg_advisory_lock(43)` (separate from auth store's 42) for Postgres migrations.
- OAuth state parameters are stored in-memory with a 10-minute expiry and 15-minute cleanup cycle. State is single-use (deleted after consumption).
- PKCE (S256) is always used for the authorization code flow per OAuth 2.1 best practices.
