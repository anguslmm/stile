package auth

import (
	"crypto/sha256"
	"testing"
)

func TestListCallers(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCaller("bob"); err != nil {
		t.Fatal(err)
	}

	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "database"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("bob", "full-access"); err != nil {
		t.Fatal(err)
	}

	h1 := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", h1, "alice-laptop"); err != nil {
		t.Fatal(err)
	}

	callers, err := store.ListCallers()
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 2 {
		t.Fatalf("expected 2 callers, got %d", len(callers))
	}

	// Sorted by name: alice, bob.
	if callers[0].Name != "alice" {
		t.Errorf("expected alice first, got %q", callers[0].Name)
	}
	if callers[0].KeyCount != 1 {
		t.Errorf("expected 1 key for alice, got %d", callers[0].KeyCount)
	}
	if len(callers[0].Roles) != 2 {
		t.Errorf("expected 2 roles for alice, got %d", len(callers[0].Roles))
	}

	if callers[1].Name != "bob" {
		t.Errorf("expected bob second, got %q", callers[1].Name)
	}
	if callers[1].KeyCount != 0 {
		t.Errorf("expected 0 keys for bob, got %d", callers[1].KeyCount)
	}
	if len(callers[1].Roles) != 1 || callers[1].Roles[0] != "full-access" {
		t.Errorf("expected [full-access] for bob, got %v", callers[1].Roles)
	}
}

func TestListCallersEmpty(t *testing.T) {
	store := newTestStore(t)

	callers, err := store.ListCallers()
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 0 {
		t.Errorf("expected 0 callers, got %d", len(callers))
	}
}

func TestKeyCountForCaller(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	count, err := store.KeyCountForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	h1 := sha256.Sum256([]byte("sk-1"))
	if err := store.AddKey("alice", h1, "key1"); err != nil {
		t.Fatal(err)
	}
	h2 := sha256.Sum256([]byte("sk-2"))
	if err := store.AddKey("alice", h2, "key2"); err != nil {
		t.Fatal(err)
	}

	count, err = store.KeyCountForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestListKeysForCaller(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	h1 := sha256.Sum256([]byte("sk-1"))
	if err := store.AddKey("alice", h1, "laptop"); err != nil {
		t.Fatal(err)
	}
	h2 := sha256.Sum256([]byte("sk-2"))
	if err := store.AddKey("alice", h2, "desktop"); err != nil {
		t.Fatal(err)
	}

	keys, err := store.ListKeysForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	labels := map[string]bool{}
	for _, k := range keys {
		labels[k.Label] = true
		if k.CreatedAt == "" {
			t.Error("expected non-empty created_at")
		}
	}
	if !labels["laptop"] || !labels["desktop"] {
		t.Errorf("expected laptop and desktop labels, got %v", keys)
	}
}

func TestRevokeKey(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	h1 := sha256.Sum256([]byte("sk-1"))
	if err := store.AddKey("alice", h1, "laptop"); err != nil {
		t.Fatal(err)
	}
	h2 := sha256.Sum256([]byte("sk-2"))
	if err := store.AddKey("alice", h2, "desktop"); err != nil {
		t.Fatal(err)
	}

	// Revoke one key.
	if err := store.RevokeKey("alice", "laptop"); err != nil {
		t.Fatal(err)
	}

	// Should have 1 key left.
	count, err := store.KeyCountForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 key after revoke, got %d", count)
	}

	// Revoked key should not work for lookup.
	_, err = store.LookupByKey(h1)
	if err == nil {
		t.Error("expected error for revoked key")
	}

	// Remaining key should still work.
	caller, err := store.LookupByKey(h2)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Name != "alice" {
		t.Errorf("expected alice, got %q", caller.Name)
	}
}

func TestRevokeKeyNotFound(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	err := store.RevokeKey("alice", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key label")
	}
}
