# Task 29 — In-memory cache for hot-path auth lookups

Status: **done**

## Problem

Every authenticated request makes 2 database round trips through the `CallerStore` interface:

1. **`LookupByKey(hash)`** — Indexed join on `api_keys`/`callers` to resolve an API key to a caller.
2. **`RolesForCaller(name)`** — Query `caller_roles` to load the caller's roles.

This data changes only via rare admin operations (add/remove callers, add/revoke keys, assign/unassign roles). At scale — especially with Postgres over a network — these round trips add unnecessary latency to every request.

Additionally, the `HasCallers()` method exists solely to allow a "no callers = auth disabled" shortcut. This is a security concern: when auth is configured but no callers exist yet, the system is wide open. Auth should be an explicit config toggle, not inferred from database state.

## Design

Two changes:

1. **Remove `HasCallers()`** — Make auth an explicit config decision. If auth is enabled and no callers exist, reject requests (don't silently disable auth).
2. **Add an optional in-memory cache** — Wrap the `CallerStore` interface with a caching layer that serves hot-path reads from memory and invalidates on writes.

### Part 1: Remove HasCallers and make auth explicit

Remove `HasCallers()` from the `CallerStore` interface and both store implementations (SQLite, Postgres). The `Authenticator.Authenticate()` method currently calls `HasCallers()` on every request to decide whether auth is active. Instead:

- Auth is **enabled by default**. The `Authenticator` is always created unless explicitly disabled in config (e.g., `auth: false`).
- If auth is enabled and a request has no valid bearer token, it is rejected — regardless of whether callers exist in the database.
- Users who don't want auth must explicitly disable it in their config.

This removes a per-request `SELECT COUNT(*)` query entirely.

### Part 2: In-memory cache

#### Cache structure

```
CachedStore
├── byKeyHash      map[[32]byte]cachedEntry[*Caller] + RWMutex
├── rolesByName    map[string]cachedEntry[[]string]  + RWMutex
├── reverseIndex   map[string]map[[32]byte]struct{}  + (shares byKeyHash mutex)
├── inner          Store (the real store)
├── ttl            time.Duration
└── notify         *pgNotifyListener (Postgres only, nil for SQLite)
```

Each `cachedEntry[T]` holds the value and a `time.Time` for TTL expiry. Expiry is checked lazily on read — no background goroutine needed.

The reverse index maps `callerName → set of cached key hashes`, built as keys are looked up. This enables targeted eviction on `DeleteKey`/`RevokeKey` (evict only that caller's cached keys, not the entire map).

#### What to cache

| Method | Cache key | Invalidated by |
|---|---|---|
| `LookupByKey(hash)` | `[32]byte` key hash | `AddKey`, `DeleteKey`, `RevokeKey`, `DeleteCaller` |
| `RolesForCaller(name)` | caller name | `AssignRole`, `UnassignRole`, `DeleteCaller` |

#### Caching strategy

**Read-through with write-through invalidation:**

- **Reads**: Check cache first. On miss (or TTL expired), fetch from inner store, populate cache, return.
- **Writes**: Delegate to inner store first. On success, evict the relevant cache entries. Never update the cache directly on writes — let the next read re-populate.

The database is always the source of truth. Writes are never cached or deferred.

#### Invalidation rules

| Write operation | Cache invalidation |
|---|---|
| `AddCaller(name)` | No eviction needed |
| `DeleteCaller(name)` | Evict all `LookupByKey` entries for that caller (via reverse index), evict `rolesByName[name]` |
| `AddKey(caller, hash, label)` | No eviction needed (new key won't be in cache yet) |
| `DeleteKey(caller, id)` | Evict all `LookupByKey` entries for that caller (via reverse index) |
| `RevokeKey(caller, label)` | Evict all `LookupByKey` entries for that caller (via reverse index) |
| `AssignRole(caller, role)` | Evict `rolesByName[caller]` |
| `UnassignRole(caller, role)` | Evict `rolesByName[caller]` |

#### Multi-instance invalidation (Postgres only)

For multi-instance deployments sharing a Postgres database, use **Postgres `LISTEN/NOTIFY`** for cross-instance cache invalidation:

- On startup, each instance runs `LISTEN stile_auth_invalidate` on a dedicated connection.
- When any instance performs a write, after the DB write succeeds, it runs `NOTIFY stile_auth_invalidate, 'keys:<caller>'` or `'roles:<caller>'`.
- All instances receive the notification and evict the relevant cache entries.

This requires no additional infrastructure — it uses the existing Postgres connection. SQLite deployments are single-instance by nature, so cross-instance invalidation is not applicable.

A configurable **TTL** (`auth_cache_ttl`) acts as a safety net in case a notification is missed. The TTL should be long-lived (default: 5 minutes) since invalidation is the primary freshness mechanism.

**Important operational note**: Revoking keys and roles takes effect immediately on the instance that performs the write. On other instances, it takes effect as soon as the Postgres notification is delivered (typically milliseconds). However, if a notification is lost (e.g., brief network partition, connection reset), a revoked key or role could remain valid in another instance's cache until the TTL expires. Operators should set `auth_cache_ttl` according to their tolerance for this worst-case staleness window.

#### Interface

```go
func NewCachedStore(inner Store, ttl time.Duration) Store
```

If `ttl == 0`, return `inner` directly — no wrapping, no maps, no goroutines, no LISTEN/NOTIFY. The feature is fully disabled.

#### Configuration

```yaml
server:
  auth_cache_ttl: 5m   # 0 or omitted to disable caching
```

#### Observability

- **Admin endpoints**: `GET /admin/cache` (stats: entry counts, hit/miss counts), `DELETE /admin/cache` (flush).
- **Metrics**: `stile_auth_cache_hits_total`, `stile_auth_cache_misses_total`, `stile_auth_cache_evictions_total` — labeled by `type` (`"keys"`, `"roles"`). These labels identify which cache map was accessed, not actual key or role values. No secrets are exposed in metrics.
- **Debug logging**: Log invalidation events and LISTEN/NOTIFY activity at debug level.

#### Future extensibility

The cache implementation uses concrete in-memory maps. If a Redis-backed cache is needed later, the cache access is isolated in a single file and can be extracted behind an interface at that point. No speculative abstraction now.

## Implementation

1. **Remove `HasCallers`**
   - Remove `HasCallers()` from `CallerStore` interface in `internal/auth/auth.go`.
   - Remove implementations from `SQLiteStore` and `PostgresStore`.
   - Update `Authenticator.Authenticate()` to always require a valid bearer token (remove the `hasCallers` check).
   - Remove `TestHasCallers` test.

2. **`internal/auth/cached_store.go`** — `CachedStore` struct implementing `Store`. Uses `sync.RWMutex` for the maps and timestamped entries for TTL expiry. Includes reverse index for targeted key eviction.

3. **`internal/auth/cached_store_test.go`** — Tests:
   - Cache hit avoids second DB call
   - Write-through invalidation evicts correct entries (targeted, not global)
   - TTL expiry causes re-fetch
   - Concurrent access safety
   - Reverse index correctness

4. **`internal/auth/pg_notify.go`** — Postgres LISTEN/NOTIFY listener. Subscribes to `stile_auth_invalidate`, parses payloads, calls eviction methods on the `CachedStore`. Only created when the inner store is `*PostgresStore`.

5. **`internal/config/config.go`** — Add `auth_cache_ttl` field to server config.

6. **`cmd/gateway/main.go`** — Wrap the store: `NewCachedStore(store, cfg.Server().AuthCacheTTL())`.

7. **Admin endpoints** — `GET /admin/cache` and `DELETE /admin/cache`.

8. **CLI remote support** — Add `stile cache show` and `stile cache flush` commands, with `--remote` support (hitting the admin API endpoints) consistent with task 28's pattern.

## What NOT to cache

- **`audit.Log()`** — Write-only, not cacheable.
- **Admin-only reads** (`ListCallers`, `GetCaller`, `ListKeys`) — Cold path, called rarely. Caching adds complexity for no meaningful gain.
- **Rate limiting** — Already in-memory (local) or Redis. Not a DB concern.

## Testing

- Unit tests with a mock inner store that counts calls, verifying cache hits/misses and invalidation.
- Benchmark comparing cached vs uncached `LookupByKey` throughput.
- Integration test: add a key via admin API, verify it's usable for auth immediately (no stale cache).
- Test that auth rejects requests when no callers exist (no more silent pass-through).
