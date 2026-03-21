package auth

import (
	"crypto/sha256"
	"fmt"
	"strings"
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
		t.Errorf("expected 2 roles for alice, got %d: %v", len(callers[0].Roles), callers[0].Roles)
	}
	if callers[0].CreatedAt.IsZero() {
		t.Error("expected non-zero created_at for alice")
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

func TestListKeys(t *testing.T) {
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

	keys, err := store.ListKeys("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	for _, k := range keys {
		if k.ID == 0 {
			t.Error("expected non-zero key ID")
		}
		if k.CreatedAt.IsZero() {
			t.Error("expected non-zero created_at")
		}
	}
	if keys[0].Label != "laptop" {
		t.Errorf("expected laptop, got %q", keys[0].Label)
	}
	if keys[1].Label != "desktop" {
		t.Errorf("expected desktop, got %q", keys[1].Label)
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

func TestGetCaller(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	h1 := sha256.Sum256([]byte("sk-1"))
	if err := store.AddKey("alice", h1, "laptop"); err != nil {
		t.Fatal(err)
	}

	detail, err := store.GetCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "alice" {
		t.Errorf("expected alice, got %q", detail.Name)
	}
	if detail.CreatedAt.IsZero() {
		t.Error("expected non-zero created_at")
	}
	if len(detail.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(detail.Keys))
	}
	if detail.Keys[0].Label != "laptop" {
		t.Errorf("expected laptop, got %q", detail.Keys[0].Label)
	}
}

func TestGetCallerNotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.GetCaller("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent caller")
	}
}

func TestDeleteKey(t *testing.T) {
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

	keys, err := store.ListKeys("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	// Delete the first key by ID.
	if err := store.DeleteKey("alice", keys[0].ID); err != nil {
		t.Fatal(err)
	}

	remaining, err := store.ListKeys("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 key after delete, got %d", len(remaining))
	}
	if remaining[0].ID != keys[1].ID {
		t.Errorf("expected key %d to remain, got %d", keys[1].ID, remaining[0].ID)
	}
}

func TestDeleteKeyNotFound(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	err := store.DeleteKey("alice", 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent key ID")
	}
}

func TestGenerateAPIKeyPropagatesError(t *testing.T) {
	orig := cryptoRandRead
	t.Cleanup(func() { cryptoRandRead = orig })

	cryptoRandRead = func(b []byte) error {
		return fmt.Errorf("entropy source unavailable")
	}

	_, err := GenerateAPIKey()
	if err == nil {
		t.Fatal("expected error when rand.Read fails")
	}
	if !strings.Contains(err.Error(), "entropy source unavailable") {
		t.Errorf("expected wrapped error, got: %v", err)
	}
}

func TestGenerateAPIKeySuccess(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(key, "sk-") {
		t.Errorf("expected sk- prefix, got %q", key)
	}
	if len(key) != 3+32 { // "sk-" + 16 bytes hex-encoded
		t.Errorf("expected length 35, got %d", len(key))
	}
}
