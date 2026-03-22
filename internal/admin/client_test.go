package admin

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/auth"
)

// newClientTestServer sets up an admin API server backed by a real SQLite store
// with admin auth, and returns the Client and the underlying store.
func newClientTestServer(t *testing.T) (*Client, *auth.SQLiteStore) {
	t.Helper()
	store := newTestStore(t)

	h := NewHandler(store, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	adminKey := "test-admin-key"
	adminHash := sha256.Sum256([]byte(adminKey))
	adminMW := auth.AdminAuthMiddleware(adminHash, store, false)
	ts := httptest.NewServer(adminMW(mux))
	t.Cleanup(ts.Close)

	client := NewClient(ts.URL, adminKey)
	return client, store
}

func TestClientAddCaller(t *testing.T) {
	client, _ := newClientTestServer(t)

	if err := client.AddCaller("alice"); err != nil {
		t.Fatalf("AddCaller: %v", err)
	}

	// Duplicate should error.
	if err := client.AddCaller("alice"); err == nil {
		t.Fatal("expected error for duplicate caller")
	}
}

func TestClientListCallers(t *testing.T) {
	client, store := newClientTestServer(t)

	store.AddCaller("alice")
	store.AddCaller("bob")
	hash := sha256.Sum256([]byte("fake-key"))
	store.AddKey("alice", hash, "laptop")
	store.AssignRole("alice", "dev")

	callers, err := client.ListCallers()
	if err != nil {
		t.Fatalf("ListCallers: %v", err)
	}
	if len(callers) != 2 {
		t.Fatalf("expected 2 callers, got %d", len(callers))
	}

	if callers[0].Name != "alice" {
		t.Errorf("expected alice first, got %q", callers[0].Name)
	}
	if callers[0].KeyCount != 1 {
		t.Errorf("expected 1 key for alice, got %d", callers[0].KeyCount)
	}
	if len(callers[0].Roles) != 1 || callers[0].Roles[0] != "dev" {
		t.Errorf("expected [dev] for alice, got %v", callers[0].Roles)
	}
}

func TestClientDeleteCaller(t *testing.T) {
	client, store := newClientTestServer(t)
	store.AddCaller("alice")

	if err := client.DeleteCaller("alice"); err != nil {
		t.Fatalf("DeleteCaller: %v", err)
	}

	if err := client.DeleteCaller("alice"); err == nil {
		t.Fatal("expected error deleting non-existent caller")
	}
}

func TestClientKeyCountForCaller(t *testing.T) {
	client, store := newClientTestServer(t)
	store.AddCaller("alice")
	hash := sha256.Sum256([]byte("k1"))
	store.AddKey("alice", hash, "laptop")

	count, err := client.KeyCountForCaller("alice")
	if err != nil {
		t.Fatalf("KeyCountForCaller: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}
}

func TestClientCreateKey(t *testing.T) {
	client, _ := newClientTestServer(t)
	client.AddCaller("alice")

	rawKey, err := client.CreateKey("alice", "CI")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if rawKey == "" || rawKey[:3] != "sk-" {
		t.Errorf("expected key starting with sk-, got %q", rawKey)
	}
}

func TestClientCreateKeyUnknownCaller(t *testing.T) {
	client, _ := newClientTestServer(t)

	_, err := client.CreateKey("nobody", "test")
	if err == nil {
		t.Fatal("expected error for unknown caller")
	}
}

func TestClientListKeys(t *testing.T) {
	client, _ := newClientTestServer(t)
	client.AddCaller("alice")
	client.CreateKey("alice", "laptop")
	client.CreateKey("alice", "CI")

	keys, err := client.ListKeys("alice")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestClientRevokeKey(t *testing.T) {
	client, _ := newClientTestServer(t)
	client.AddCaller("alice")
	client.CreateKey("alice", "laptop")
	client.CreateKey("alice", "CI")

	if err := client.RevokeKey("alice", "laptop"); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	keys, err := client.ListKeys("alice")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key after revoke, got %d", len(keys))
	}
	if keys[0].Label != "CI" {
		t.Errorf("expected CI key to remain, got %q", keys[0].Label)
	}
}

func TestClientRevokeKeyNotFound(t *testing.T) {
	client, _ := newClientTestServer(t)
	client.AddCaller("alice")

	if err := client.RevokeKey("alice", "nonexistent"); err == nil {
		t.Fatal("expected error revoking non-existent key")
	}
}

func TestClientAssignRole(t *testing.T) {
	client, store := newClientTestServer(t)
	store.AddCaller("alice")

	if err := client.AssignRole("alice", "dev"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	roles, _ := store.RolesForCaller("alice")
	if len(roles) != 1 || roles[0] != "dev" {
		t.Errorf("expected [dev], got %v", roles)
	}
}

func TestClientAssignRoleUnknownCaller(t *testing.T) {
	client, _ := newClientTestServer(t)

	if err := client.AssignRole("nobody", "dev"); err == nil {
		t.Fatal("expected error for unknown caller")
	}
}

func TestClientUnassignRole(t *testing.T) {
	client, store := newClientTestServer(t)
	store.AddCaller("alice")
	store.AssignRole("alice", "dev")

	if err := client.UnassignRole("alice", "dev"); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}

	roles, _ := store.RolesForCaller("alice")
	if len(roles) != 0 {
		t.Errorf("expected no roles, got %v", roles)
	}
}

func TestClientUnassignRoleNotAssigned(t *testing.T) {
	client, store := newClientTestServer(t)
	store.AddCaller("alice")

	if err := client.UnassignRole("alice", "nonexistent"); err == nil {
		t.Fatal("expected error unassigning non-existent role")
	}
}

func TestClientAuthRequired(t *testing.T) {
	store := newTestStore(t)
	store.AddCaller("existing")

	h := NewHandler(store, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	adminKey := "real-key"
	adminHash := sha256.Sum256([]byte(adminKey))
	adminMW := auth.AdminAuthMiddleware(adminHash, store, false)
	ts := httptest.NewServer(adminMW(mux))
	defer ts.Close()

	// Wrong key should fail.
	badClient := NewClient(ts.URL, "wrong-key")
	_, err := badClient.ListCallers()
	if err == nil {
		t.Fatal("expected error with wrong admin key")
	}

	// Correct key should work.
	goodClient := NewClient(ts.URL, adminKey)
	callers, err := goodClient.ListCallers()
	if err != nil {
		t.Fatalf("ListCallers with correct key: %v", err)
	}
	if len(callers) != 1 {
		t.Errorf("expected 1 caller, got %d", len(callers))
	}
}

func TestClientConnectionError(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "key")
	_, err := client.ListCallers()
	if err == nil {
		t.Fatal("expected connection error")
	}
	if !strings.Contains(err.Error(), "cannot reach remote") {
		t.Errorf("expected 'cannot reach remote' in error, got: %s", err)
	}
}

func TestClientErrorParsing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "caller already exists"})
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "key")
	err := client.AddCaller("dup")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "caller already exists") {
		t.Errorf("expected error message from server, got: %s", err)
	}
}
