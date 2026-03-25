package auth

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockStore is a minimal Store that counts calls to hot-path methods.
type mockStore struct {
	lookupCalls atomic.Int64
	rolesCalls  atomic.Int64

	mu      sync.Mutex
	callers map[string]bool
	keys    map[[32]byte]string // hash → caller name
	roles   map[string][]string // caller name → roles
}

func newMockStore() *mockStore {
	return &mockStore{
		callers: make(map[string]bool),
		keys:    make(map[[32]byte]string),
		roles:   make(map[string][]string),
	}
}

func (m *mockStore) LookupByKey(h [32]byte) (*KeyLookupResult, error) {
	m.lookupCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	name, ok := m.keys[h]
	if !ok {
		return nil, fmt.Errorf("auth: key not found: %w", ErrNotFound)
	}
	return &KeyLookupResult{Caller: &Caller{Name: name}}, nil
}

func (m *mockStore) RolesForCaller(name string) ([]string, error) {
	m.rolesCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.roles[name], nil
}

func (m *mockStore) AddCaller(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callers[name] = true
	return nil
}

func (m *mockStore) DeleteCaller(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.callers, name)
	// Remove keys for this caller.
	for h, c := range m.keys {
		if c == name {
			delete(m.keys, h)
		}
	}
	delete(m.roles, name)
	return nil
}

func (m *mockStore) AddKey(callerName string, keyHash [32]byte, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[keyHash] = callerName
	return nil
}

func (m *mockStore) DeleteKey(callerName string, keyID int64) error    { return nil }
func (m *mockStore) RevokeKey(callerName string, label string) error   { return nil }
func (m *mockStore) ListCallers() ([]CallerInfo, error)                { return nil, nil }
func (m *mockStore) GetCaller(name string) (*CallerDetail, error)      { return nil, nil }
func (m *mockStore) ListKeys(callerName string) ([]KeyInfo, error)     { return nil, nil }
func (m *mockStore) KeyCountForCaller(callerName string) (int, error)  { return 0, nil }
func (m *mockStore) Close() error                                      { return nil }

func (m *mockStore) AssignRole(callerName string, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roles[callerName] = append(m.roles[callerName], role)
	return nil
}

func (m *mockStore) UnassignRole(callerName string, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	roles := m.roles[callerName]
	for i, r := range roles {
		if r == role {
			m.roles[callerName] = append(roles[:i], roles[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("auth: not found: %w", ErrNotFound)
}

func (m *mockStore) EnsureCaller(name string, defaultRoles []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.callers[name] {
		m.callers[name] = true
		m.roles[name] = append(m.roles[name], defaultRoles...)
	}
	return nil
}

func (m *mockStore) CallerExists(name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callers[name], nil
}

var _ Store = (*mockStore)(nil)

func TestCacheHitAvoidsSecondDBCall(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	hash := sha256.Sum256([]byte("sk-alice"))
	mock.AddKey("alice", hash, "")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	// First call: miss → goes to DB.
	result, err := store.LookupByKey(hash)
	if err != nil {
		t.Fatal(err)
	}
	if result.Caller.Name != "alice" {
		t.Fatalf("expected alice, got %q", result.Caller.Name)
	}
	if mock.lookupCalls.Load() != 1 {
		t.Fatalf("expected 1 DB call, got %d", mock.lookupCalls.Load())
	}

	// Second call: hit → no DB call.
	result, err = store.LookupByKey(hash)
	if err != nil {
		t.Fatal(err)
	}
	if result.Caller.Name != "alice" {
		t.Fatalf("expected alice, got %q", result.Caller.Name)
	}
	if mock.lookupCalls.Load() != 1 {
		t.Fatalf("expected still 1 DB call (cache hit), got %d", mock.lookupCalls.Load())
	}

	stats := store.Stats()
	if stats.KeyHits != 1 {
		t.Errorf("expected 1 key hit, got %d", stats.KeyHits)
	}
	if stats.KeyMisses != 1 {
		t.Errorf("expected 1 key miss, got %d", stats.KeyMisses)
	}
}

func TestCacheRolesHit(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	mock.AssignRole("alice", "admin")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	roles, err := store.RolesForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || roles[0] != "admin" {
		t.Fatalf("expected [admin], got %v", roles)
	}

	// Second call: cache hit.
	roles, err = store.RolesForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if mock.rolesCalls.Load() != 1 {
		t.Fatalf("expected 1 DB call, got %d", mock.rolesCalls.Load())
	}

	stats := store.Stats()
	if stats.RoleHits != 1 {
		t.Errorf("expected 1 role hit, got %d", stats.RoleHits)
	}
}

func TestWriteThroughInvalidation(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	hash := sha256.Sum256([]byte("sk-alice"))
	mock.AddKey("alice", hash, "")
	mock.AssignRole("alice", "admin")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	// Populate cache.
	store.LookupByKey(hash)
	store.RolesForCaller("alice")

	// Delete caller → should evict both key and role caches.
	if err := store.DeleteCaller("alice"); err != nil {
		t.Fatal(err)
	}

	// Next lookup should miss (caller was deleted in mock too).
	_, err := store.LookupByKey(hash)
	if err == nil {
		t.Fatal("expected error after deletion")
	}
	if mock.lookupCalls.Load() != 2 {
		t.Fatalf("expected 2 DB calls (1 original + 1 after eviction), got %d", mock.lookupCalls.Load())
	}

	stats := store.Stats()
	if stats.Evictions == 0 {
		t.Error("expected evictions > 0")
	}
}

func TestAssignRoleEvictsRoleCache(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	mock.AssignRole("alice", "admin")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	// Populate roles cache.
	store.RolesForCaller("alice")
	if mock.rolesCalls.Load() != 1 {
		t.Fatalf("expected 1 DB call, got %d", mock.rolesCalls.Load())
	}

	// Assign another role → evicts cached roles.
	// store.AssignRole delegates to mock, then evicts cache.
	store.AssignRole("alice", "editor")

	// Next call should miss (evicted).
	roles, _ := store.RolesForCaller("alice")
	if mock.rolesCalls.Load() != 2 {
		t.Fatalf("expected 2 DB calls after eviction, got %d", mock.rolesCalls.Load())
	}
	if len(roles) != 2 {
		t.Errorf("expected 2 roles, got %v", roles)
	}
}

func TestTTLExpiryCausesRefetch(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	hash := sha256.Sum256([]byte("sk-alice"))
	mock.AddKey("alice", hash, "")

	store := NewCachedStore(mock, 100*time.Millisecond).(*CachedStore)

	// Use a controllable clock.
	now := time.Now()
	store.now = func() time.Time { return now }

	store.LookupByKey(hash)
	if mock.lookupCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", mock.lookupCalls.Load())
	}

	// Advance time past TTL.
	now = now.Add(200 * time.Millisecond)

	store.LookupByKey(hash)
	if mock.lookupCalls.Load() != 2 {
		t.Fatalf("expected 2 calls after TTL expiry, got %d", mock.lookupCalls.Load())
	}
}

func TestReverseIndexCorrectness(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	mock.AddCaller("bob")

	aliceHash1 := sha256.Sum256([]byte("sk-alice-1"))
	aliceHash2 := sha256.Sum256([]byte("sk-alice-2"))
	bobHash := sha256.Sum256([]byte("sk-bob"))
	mock.AddKey("alice", aliceHash1, "")
	mock.AddKey("alice", aliceHash2, "")
	mock.AddKey("bob", bobHash, "")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	// Populate cache for all keys.
	store.LookupByKey(aliceHash1)
	store.LookupByKey(aliceHash2)
	store.LookupByKey(bobHash)

	if store.Stats().KeyEntries != 3 {
		t.Fatalf("expected 3 key entries, got %d", store.Stats().KeyEntries)
	}

	// Evict alice's keys via RevokeKey (evicts by caller name).
	store.RevokeKey("alice", "")

	// Alice's keys should be evicted, bob's should remain.
	if store.Stats().KeyEntries != 1 {
		t.Fatalf("expected 1 key entry after alice eviction, got %d", store.Stats().KeyEntries)
	}

	// Bob's key should still be cached.
	store.LookupByKey(bobHash)
	if mock.lookupCalls.Load() != 3 {
		t.Fatalf("expected 3 total DB calls (bob still cached), got %d", mock.lookupCalls.Load())
	}
}

func TestConcurrentAccess(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	hash := sha256.Sum256([]byte("sk-alice"))
	mock.AddKey("alice", hash, "")
	mock.AssignRole("alice", "admin")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			store.LookupByKey(hash)
		}()
		go func() {
			defer wg.Done()
			store.RolesForCaller("alice")
		}()
	}
	wg.Wait()

	// No panics, and stats should be consistent.
	stats := store.Stats()
	totalKey := stats.KeyHits + stats.KeyMisses
	if totalKey != 100 {
		t.Errorf("expected 100 total key operations, got %d", totalKey)
	}
	totalRole := stats.RoleHits + stats.RoleMisses
	if totalRole != 100 {
		t.Errorf("expected 100 total role operations, got %d", totalRole)
	}
}

func TestFlush(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	hash := sha256.Sum256([]byte("sk-alice"))
	mock.AddKey("alice", hash, "")
	mock.AssignRole("alice", "admin")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	store.LookupByKey(hash)
	store.RolesForCaller("alice")

	if store.Stats().KeyEntries != 1 || store.Stats().RoleEntries != 1 {
		t.Fatal("expected cache to have entries")
	}

	store.Flush()

	if store.Stats().KeyEntries != 0 || store.Stats().RoleEntries != 0 {
		t.Fatal("expected cache to be empty after flush")
	}

	// Subsequent lookups should hit the DB again.
	store.LookupByKey(hash)
	if mock.lookupCalls.Load() != 2 {
		t.Fatalf("expected 2 DB calls after flush, got %d", mock.lookupCalls.Load())
	}
}

func TestZeroTTLReturnsInnerDirectly(t *testing.T) {
	mock := newMockStore()
	store := NewCachedStore(mock, 0)
	if _, ok := store.(*CachedStore); ok {
		t.Fatal("expected inner store to be returned directly with ttl=0")
	}
}

func TestRolesReturnsCopy(t *testing.T) {
	mock := newMockStore()
	mock.AddCaller("alice")
	mock.AssignRole("alice", "admin")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	roles1, _ := store.RolesForCaller("alice")
	roles1[0] = "mutated"

	roles2, _ := store.RolesForCaller("alice")
	if roles2[0] == "mutated" {
		t.Fatal("cache returned reference instead of copy; mutation leaked")
	}
}

func BenchmarkCachedLookup(b *testing.B) {
	mock := newMockStore()
	mock.AddCaller("alice")
	hash := sha256.Sum256([]byte("sk-alice"))
	mock.AddKey("alice", hash, "")

	store := NewCachedStore(mock, 5*time.Minute).(*CachedStore)

	// Pre-populate cache.
	store.LookupByKey(hash)

	b.ResetTimer()
	for b.Loop() {
		store.LookupByKey(hash)
	}
}
