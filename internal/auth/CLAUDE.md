# auth

Inbound API key authentication, role-based tool ACLs, outbound credential injection, and caller/key/role management backed by SQLite or Postgres.

## Key Types

- **`Caller`** — Authenticated identity with name, roles, and compiled glob patterns for tool access. `CanAccessTool(name)` checks ACL.
- **`CallerStore`** (interface) — Read-only hot path: `LookupByKey([32]byte)` and `RolesForCaller(name)`. Used by `Authenticator`.
- **`Store`** (interface) — Full management interface extending `CallerStore`: add/delete callers and keys, assign/unassign roles, list/revoke.
- **`Authenticator`** — Validates inbound Bearer tokens (SHA-256 hash lookup), resolves roles, computes union of tool globs, and resolves outbound upstream credentials from env vars.
- **`SQLiteStore`** — `Store` implementation backed by SQLite (via `modernc.org/sqlite`).
- **`PostgresStore`** — `Store` implementation backed by Postgres (via `pgx/v5/stdlib`).
- **`CachedStore`** — Write-through cache wrapping any `Store`. Caches `LookupByKey` and `RolesForCaller` with TTL. Evicts on writes via reverse index (callerName → key hashes).
- **`PGNotifyListener`** — Listens on Postgres channel `stile_auth_invalidate` and evicts `CachedStore` entries. Enables cache coherence across multiple Stile instances.
- **`CacheStats`** / **`Cacheable`** — Observability interface and stats struct for cache hit/miss/eviction counters.

## Key Functions

- `OpenStore(cfg)` — Factory: returns `SQLiteStore` or `PostgresStore` based on driver config.
- `NewCachedStore(inner, ttl)` — Wraps a store with caching; returns `inner` directly if `ttl == 0`.
- `NewAuthenticator(store, roles)` — Compiles role configs: reads credential env vars, compiles tool glob patterns.
- `GenerateAPIKey()` — Returns a `"sk-"` prefixed 32-hex-char random key.
- `CallerFromContext(ctx)` / `ContextWithCaller(ctx, c)` — Context helpers for propagating `Caller` through the request pipeline.
- `AdminAuthMiddleware(keyHash, devMode)` — Separate middleware for admin endpoints; uses `crypto/subtle` constant-time compare.

## Design Notes

- API keys are never stored in plaintext — only SHA-256 hashes. `LookupByKey` takes `[32]byte`.
- Roles are stored in the DB; glob patterns and outbound credentials are in config/env. The `Authenticator` joins them at auth time.
- Role ordering in `Caller.Roles` follows config order (not DB order). `UpstreamToken` picks the first matching role.
- `CachedStore` evicts by caller name using a reverse index; it never updates cache entries in place.
- SQLite uses WAL mode + foreign keys + busy timeout set via DSN pragma. Includes a destructive migration for pre-6.2 schemas missing `caller_roles`.
- Postgres migrations use `pg_advisory_lock(42)` to prevent concurrent `CREATE TABLE` races on multi-instance startup.
- `PGNotifyListener` uses a dedicated `pgx` connection for `LISTEN` (separate from the `sql.DB` pool) and reconnects with 1s backoff.
