package auth

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// CacheStats holds cache statistics for observability.
type CacheStats struct {
	KeyEntries  int   `json:"key_entries"`
	RoleEntries int   `json:"role_entries"`
	KeyHits     int64 `json:"key_hits"`
	KeyMisses   int64 `json:"key_misses"`
	RoleHits    int64 `json:"role_hits"`
	RoleMisses  int64 `json:"role_misses"`
	Evictions   int64 `json:"evictions"`
}

// Cacheable is implemented by stores that support caching.
type Cacheable interface {
	Stats() CacheStats
	Flush()
}

type cachedEntry[T any] struct {
	value     T
	expiresAt time.Time
}

func (e cachedEntry[T]) expired(now time.Time) bool {
	return now.After(e.expiresAt)
}

// CacheNotifyFunc is called after write operations and flush to broadcast
// invalidation to other instances. kind is "keys", "roles", or "flush".
type CacheNotifyFunc func(kind, callerName string)

// CachedStore wraps a Store with an in-memory cache for LookupByKey and
// RolesForCaller. Writes are delegated to the inner store first; on success,
// relevant cache entries are evicted (never updated directly).
type CachedStore struct {
	inner  Store
	ttl    time.Duration
	now    func() time.Time // for testing
	notify CacheNotifyFunc  // nil = no cross-instance notification

	keyMu        sync.RWMutex
	byKeyHash    map[[32]byte]cachedEntry[*KeyLookupResult]
	reverseIndex map[string]map[[32]byte]struct{} // callerName → set of cached key hashes

	roleMu     sync.RWMutex
	rolesByName map[string]cachedEntry[[]string]

	keyHits    atomic.Int64
	keyMisses  atomic.Int64
	roleHits   atomic.Int64
	roleMisses atomic.Int64
	evictions  atomic.Int64
}

var _ Store = (*CachedStore)(nil)

// NewCachedStore wraps inner with an in-memory cache. If ttl is 0, inner is
// returned directly — no wrapping, no maps, no overhead.
func NewCachedStore(inner Store, ttl time.Duration) Store {
	if ttl == 0 {
		return inner
	}
	return &CachedStore{
		inner:        inner,
		ttl:          ttl,
		now:          time.Now,
		byKeyHash:    make(map[[32]byte]cachedEntry[*KeyLookupResult]),
		reverseIndex: make(map[string]map[[32]byte]struct{}),
		rolesByName:  make(map[string]cachedEntry[[]string]),
	}
}

// SetNotify sets the cross-instance notification callback. Must be called
// before the store starts serving requests.
func (c *CachedStore) SetNotify(fn CacheNotifyFunc) {
	c.notify = fn
}

func (c *CachedStore) sendNotify(kind, callerName string) {
	if c.notify != nil {
		go c.notify(kind, callerName)
	}
}

// --- CallerStore (hot path) ---

func (c *CachedStore) LookupByKey(hashedKey [32]byte) (*KeyLookupResult, error) {
	now := c.now()

	c.keyMu.RLock()
	entry, ok := c.byKeyHash[hashedKey]
	c.keyMu.RUnlock()

	if ok && !entry.expired(now) {
		c.keyHits.Add(1)
		return entry.value, nil
	}

	c.keyMisses.Add(1)
	result, err := c.inner.LookupByKey(hashedKey)
	if err != nil {
		return nil, err
	}

	c.keyMu.Lock()
	c.byKeyHash[hashedKey] = cachedEntry[*KeyLookupResult]{
		value:     result,
		expiresAt: now.Add(c.ttl),
	}
	// Build reverse index: callerName → set of key hashes.
	if c.reverseIndex[result.Caller.Name] == nil {
		c.reverseIndex[result.Caller.Name] = make(map[[32]byte]struct{})
	}
	c.reverseIndex[result.Caller.Name][hashedKey] = struct{}{}
	c.keyMu.Unlock()

	return result, nil
}

func (c *CachedStore) RolesForCaller(name string) ([]string, error) {
	now := c.now()

	c.roleMu.RLock()
	entry, ok := c.rolesByName[name]
	c.roleMu.RUnlock()

	if ok && !entry.expired(now) {
		c.roleHits.Add(1)
		// Return a copy to prevent caller mutation.
		out := make([]string, len(entry.value))
		copy(out, entry.value)
		return out, nil
	}

	c.roleMisses.Add(1)
	roles, err := c.inner.RolesForCaller(name)
	if err != nil {
		return nil, err
	}

	// Store a copy so the cached value is immutable.
	cached := make([]string, len(roles))
	copy(cached, roles)

	c.roleMu.Lock()
	c.rolesByName[name] = cachedEntry[[]string]{
		value:     cached,
		expiresAt: now.Add(c.ttl),
	}
	c.roleMu.Unlock()

	return roles, nil
}

// --- Write-through operations (delegate then evict) ---

func (c *CachedStore) AddCaller(name string) error {
	return c.inner.AddCaller(name)
}

func (c *CachedStore) DeleteCaller(name string) error {
	err := c.inner.DeleteCaller(name)
	if err != nil {
		return err
	}
	c.evictKeysForCaller(name)
	c.evictRoles(name)
	c.sendNotify("keys", name)
	c.sendNotify("roles", name)
	return nil
}

func (c *CachedStore) AddKey(callerName string, keyHash [32]byte, label string) error {
	return c.inner.AddKey(callerName, keyHash, label)
}

func (c *CachedStore) DeleteKey(callerName string, keyID int64) error {
	err := c.inner.DeleteKey(callerName, keyID)
	if err != nil {
		return err
	}
	c.evictKeysForCaller(callerName)
	c.sendNotify("keys", callerName)
	return nil
}

func (c *CachedStore) RevokeKey(callerName string, label string) error {
	err := c.inner.RevokeKey(callerName, label)
	if err != nil {
		return err
	}
	c.evictKeysForCaller(callerName)
	c.sendNotify("keys", callerName)
	return nil
}

func (c *CachedStore) AssignRole(callerName string, role string) error {
	err := c.inner.AssignRole(callerName, role)
	if err != nil {
		return err
	}
	c.evictRoles(callerName)
	c.sendNotify("roles", callerName)
	return nil
}

func (c *CachedStore) UnassignRole(callerName string, role string) error {
	err := c.inner.UnassignRole(callerName, role)
	if err != nil {
		return err
	}
	c.evictRoles(callerName)
	c.sendNotify("roles", callerName)
	return nil
}

// --- OIDC provisioning ---

func (c *CachedStore) EnsureCaller(name string, defaultRoles []string) error {
	err := c.inner.EnsureCaller(name, defaultRoles)
	if err != nil {
		return err
	}
	c.evictRoles(name)
	c.sendNotify("roles", name)
	return nil
}

func (c *CachedStore) CallerExists(name string) (bool, error) {
	return c.inner.CallerExists(name)
}

// --- Passthrough (cold path, never cached) ---

func (c *CachedStore) ListCallers() ([]CallerInfo, error)           { return c.inner.ListCallers() }
func (c *CachedStore) GetCaller(name string) (*CallerDetail, error) { return c.inner.GetCaller(name) }
func (c *CachedStore) ListKeys(callerName string) ([]KeyInfo, error) {
	return c.inner.ListKeys(callerName)
}
func (c *CachedStore) KeyCountForCaller(callerName string) (int, error) {
	return c.inner.KeyCountForCaller(callerName)
}
func (c *CachedStore) Close() error { return c.inner.Close() }

// --- Eviction ---

func (c *CachedStore) evictKeysForCaller(callerName string) {
	c.keyMu.Lock()
	hashes := c.reverseIndex[callerName]
	count := len(hashes)
	for h := range hashes {
		delete(c.byKeyHash, h)
	}
	delete(c.reverseIndex, callerName)
	c.keyMu.Unlock()

	if count > 0 {
		c.evictions.Add(int64(count))
		slog.Debug("cache evicted keys", "caller", callerName, "count", count)
	}
}

func (c *CachedStore) evictRoles(callerName string) {
	c.roleMu.Lock()
	_, existed := c.rolesByName[callerName]
	delete(c.rolesByName, callerName)
	c.roleMu.Unlock()

	if existed {
		c.evictions.Add(1)
		slog.Debug("cache evicted roles", "caller", callerName)
	}
}

// EvictKeys evicts cached LookupByKey entries for a caller. Exported for
// use by the Postgres LISTEN/NOTIFY listener.
func (c *CachedStore) EvictKeys(callerName string) {
	c.evictKeysForCaller(callerName)
}

// EvictRoles evicts cached RolesForCaller entries for a caller. Exported for
// use by the Postgres LISTEN/NOTIFY listener.
func (c *CachedStore) EvictRoles(callerName string) {
	c.evictRoles(callerName)
}

// --- Observability ---

func (c *CachedStore) Stats() CacheStats {
	c.keyMu.RLock()
	keyCount := len(c.byKeyHash)
	c.keyMu.RUnlock()

	c.roleMu.RLock()
	roleCount := len(c.rolesByName)
	c.roleMu.RUnlock()

	return CacheStats{
		KeyEntries:  keyCount,
		RoleEntries: roleCount,
		KeyHits:     c.keyHits.Load(),
		KeyMisses:   c.keyMisses.Load(),
		RoleHits:    c.roleHits.Load(),
		RoleMisses:  c.roleMisses.Load(),
		Evictions:   c.evictions.Load(),
	}
}

// Flush clears all cached entries and broadcasts to other instances.
func (c *CachedStore) Flush() {
	c.flushLocal()
	c.sendNotify("flush", "")
}

// flushLocal clears all cached entries without broadcasting.
func (c *CachedStore) flushLocal() {
	c.keyMu.Lock()
	evicted := len(c.byKeyHash)
	c.byKeyHash = make(map[[32]byte]cachedEntry[*KeyLookupResult])
	c.reverseIndex = make(map[string]map[[32]byte]struct{})
	c.keyMu.Unlock()

	c.roleMu.Lock()
	evicted += len(c.rolesByName)
	c.rolesByName = make(map[string]cachedEntry[[]string])
	c.roleMu.Unlock()

	c.evictions.Add(int64(evicted))
	slog.Debug("cache flushed", "evicted", evicted)
}
