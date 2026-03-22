# Task 29 — In-memory cache for hot-path auth lookups

Status: **todo**

## Problem

Every authenticated request makes 2-3 database round trips through the `CallerStore` interface:

1. **`HasCallers()`** — `COUNT(*)` on callers to check if auth is enabled.
2. **`LookupByKey(hash)`** — Indexed join on `api_keys`/`callers` to resolve an API key to a caller.
3. **`RolesForCaller(name)`** — Query `caller_roles` to load the caller's roles.

This data changes only via rare admin operations (add/remove callers, add/revoke keys, assign/unassign roles). At scale — especially with Postgres over a network — these round trips add unnecessary latency to every request.

## Design

Wrap the `CallerStore` interface with a caching layer that serves hot-path reads from memory and invalidates on writes.

### Cache structure

```
CachedCallerStore
├── hasCallers     atomic.Bool (or *bool for tri-state: unknown/true/false)
├── byKeyHash      sync.Map or map[[32]byte]*Caller + RWMutex
├── rolesByName    sync.Map or map[string][]string + RWMutex
└── inner          CallerStore (the real store)
```

### What to cache

| Method | Cache key | Cache type | TTL | Invalidated by |
|---|---|---|---|---|
| `HasCallers()` | singleton | `atomic.Bool` | none (invalidation only) | `AddCaller`, `DeleteCaller` |
| `LookupByKey(hash)` | `[32]byte` key hash | map | none (invalidation only) | `AddKey`, `DeleteKey`, `RevokeKey`, `DeleteCaller` |
| `RolesForCaller(name)` | caller name | map | none (invalidation only) | `AssignRole`, `UnassignRole`, `DeleteCaller` |

### Invalidation strategy

**Write-through invalidation**: The `CachedCallerStore` also wraps the full `Store` interface. When a write method is called, it delegates to the inner store and then invalidates the relevant cache entries.

| Write operation | Invalidation |
|---|---|
| `AddCaller(name)` | Set `hasCallers = true` |
| `DeleteCaller(name)` | Evict all `LookupByKey` entries for that caller, evict `rolesByName[name]`, recompute `hasCallers` |
| `AddKey(caller, hash, label)` | No eviction needed (new key won't be in cache yet) |
| `DeleteKey(caller, id)` | Evict all `LookupByKey` entries for that caller (we don't know which hash) |
| `RevokeKey(caller, label)` | Evict all `LookupByKey` entries for that caller |
| `AssignRole(caller, role)` | Evict `rolesByName[caller]` |
| `UnassignRole(caller, role)` | Evict `rolesByName[caller]` |

For `DeleteKey`/`RevokeKey`/`DeleteCaller`, we need to evict LookupByKey entries by caller name rather than by hash. This means we need a reverse index from caller name → set of cached key hashes, or we simply clear the entire key cache on these operations. Given that key deletion is very rare, clearing the whole key cache is the simpler and safer approach.

### Multi-instance invalidation

With multiple Stile instances pointing at the same database, one instance's admin write won't invalidate another's cache. Options:

1. **TTL fallback**: Add a configurable max-age (default: e.g. 60s) so stale entries are eventually refreshed. This is the simplest approach.
2. **Document the limitation**: Admin operations should be followed by hitting each instance's `/admin/refresh` endpoint (or a future cache-clear endpoint).

Recommend option 1 — a TTL provides an upper bound on staleness without operational burden. The TTL should be configurable:

```yaml
server:
  auth_cache_ttl: 60s   # 0 to disable caching
```

### Interface

The `CachedCallerStore` should implement `Store` (not just `CallerStore`) so it can intercept writes for invalidation. It wraps an inner `Store`.

```go
func NewCachedStore(inner Store, ttl time.Duration) Store
```

If `ttl == 0`, return `inner` directly (no caching).

## Implementation

1. **`internal/auth/cached_store.go`** — `CachedStore` struct implementing `Store`. Uses `sync.RWMutex` for the maps and timestamped entries for TTL expiry.

2. **`internal/auth/cached_store_test.go`** — Tests:
   - Cache hit avoids second DB call
   - Write-through invalidation evicts correct entries
   - TTL expiry causes re-fetch
   - Concurrent access safety

3. **`internal/config/config.go`** — Add `auth_cache_ttl` field to server config.

4. **`cmd/gateway/main.go`** — Wrap the store with `NewCachedStore(store, cfg.Server().AuthCacheTTL())` during wiring.

## What NOT to cache

- **`audit.Log()`** — Write-only, not cacheable.
- **Admin-only reads** (`ListCallers`, `GetCaller`, `ListKeys`) — Cold path, called rarely. Caching adds complexity for no meaningful gain.
- **Rate limiting** — Already in-memory (local) or Redis. Not a DB concern.

## Testing

- Unit tests with a mock inner store that counts calls, verifying cache hits/misses and invalidation.
- Benchmark comparing cached vs uncached `LookupByKey` throughput.
- Integration test: add a key via admin API, verify it's usable for auth immediately (no stale cache).
