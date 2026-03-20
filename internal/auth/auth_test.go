package auth

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/anguslmm/stile/internal/config"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// loadRoles is a helper to load role config from YAML.
func loadRoles(t *testing.T, yaml string) []config.RoleConfig {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Roles()
}

// --- CallerStore tests ---

func TestAddCallerAndLookup(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	key := "sk-test-key-alice"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, "alice-web"); err != nil {
		t.Fatal(err)
	}

	caller, err := store.LookupByKey(hash)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Name != "alice" {
		t.Errorf("name = %q, want alice", caller.Name)
	}
	// LookupByKey returns name only — no roles or AllowedTools.
	if len(caller.Roles) != 0 {
		t.Errorf("expected no Roles from store, got %d", len(caller.Roles))
	}
	if len(caller.AllowedTools) != 0 {
		t.Errorf("expected no AllowedTools from store, got %d", len(caller.AllowedTools))
	}
}

func TestUnknownKeyReturnsError(t *testing.T) {
	store := newTestStore(t)

	hash := sha256.Sum256([]byte("nonexistent-key"))
	_, err := store.LookupByKey(hash)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestMultipleKeysSameCaller(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("bob"); err != nil {
		t.Fatal(err)
	}

	webKey := "sk-bob-web"
	webHash := sha256.Sum256([]byte(webKey))
	if err := store.AddKey("bob", webHash, "bob-web"); err != nil {
		t.Fatal(err)
	}

	dbKey := "sk-bob-db"
	dbHash := sha256.Sum256([]byte(dbKey))
	if err := store.AddKey("bob", dbHash, "bob-db"); err != nil {
		t.Fatal(err)
	}

	// Both keys resolve to the same caller.
	caller, err := store.LookupByKey(webHash)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Name != "bob" {
		t.Errorf("web key caller = %q, want bob", caller.Name)
	}

	caller, err = store.LookupByKey(dbHash)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Name != "bob" {
		t.Errorf("db key caller = %q, want bob", caller.Name)
	}
}

func TestMultipleCallers(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCaller("bob"); err != nil {
		t.Fatal(err)
	}

	aliceHash := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", aliceHash, ""); err != nil {
		t.Fatal(err)
	}

	bobHash := sha256.Sum256([]byte("sk-bob"))
	if err := store.AddKey("bob", bobHash, ""); err != nil {
		t.Fatal(err)
	}

	alice, err := store.LookupByKey(aliceHash)
	if err != nil {
		t.Fatal(err)
	}
	if alice.Name != "alice" {
		t.Errorf("expected alice, got %q", alice.Name)
	}

	bob, err := store.LookupByKey(bobHash)
	if err != nil {
		t.Fatal(err)
	}
	if bob.Name != "bob" {
		t.Errorf("expected bob, got %q", bob.Name)
	}
}

func TestDeletedCaller(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("charlie"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-charlie"))
	if err := store.AddKey("charlie", hash, ""); err != nil {
		t.Fatal(err)
	}

	// Verify it works first.
	if _, err := store.LookupByKey(hash); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteCaller("charlie"); err != nil {
		t.Fatal(err)
	}

	_, err := store.LookupByKey(hash)
	if err == nil {
		t.Fatal("expected error after caller deletion")
	}
}

func TestDeletedCallerCascadesRoles(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("charlie"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("charlie", "web-tools"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("charlie", "database"); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteCaller("charlie"); err != nil {
		t.Fatal(err)
	}

	// Re-create charlie — should have no roles.
	if err := store.AddCaller("charlie"); err != nil {
		t.Fatal(err)
	}
	roles, err := store.RolesForCaller("charlie")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 0 {
		t.Errorf("expected no roles after delete+recreate, got %v", roles)
	}
}

func TestHasCallers(t *testing.T) {
	store := newTestStore(t)

	has, err := store.HasCallers()
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("expected no callers in fresh database")
	}

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	has, err = store.HasCallers()
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("expected callers after insert")
	}
}

// --- AssignRole / UnassignRole / RolesForCaller tests ---

func TestAssignRole(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}

	roles, err := store.RolesForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || roles[0] != "web-tools" {
		t.Errorf("expected [web-tools], got %v", roles)
	}
}

func TestAssignRoleIdempotent(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}
	// Assign same role again — should not error.
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatalf("expected no error for idempotent assign, got: %v", err)
	}

	roles, err := store.RolesForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 {
		t.Errorf("expected 1 role after idempotent assign, got %d", len(roles))
	}
}

func TestUnassignRole(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "database"); err != nil {
		t.Fatal(err)
	}

	if err := store.UnassignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}

	roles, err := store.RolesForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || roles[0] != "database" {
		t.Errorf("expected [database], got %v", roles)
	}
}

func TestUnassignRoleNonexistent(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	err := store.UnassignRole("alice", "nonexistent")
	if err == nil {
		t.Fatal("expected error for unassigning nonexistent role")
	}
}

func TestRolesForCaller(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "database"); err != nil {
		t.Fatal(err)
	}

	roles, err := store.RolesForCaller("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(roles))
	}

	roleSet := map[string]bool{}
	for _, r := range roles {
		roleSet[r] = true
	}
	if !roleSet["web-tools"] || !roleSet["database"] {
		t.Errorf("expected web-tools and database roles, got %v", roles)
	}
}

func TestAddKeyWithoutRole(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	caller, err := store.LookupByKey(hash)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Name != "alice" {
		t.Errorf("expected alice, got %q", caller.Name)
	}
}

// --- Role-based access control tests ---

func TestCallerGetsUnionOfAllRoles(t *testing.T) {
	store := newTestStore(t)
	roles := loadRoles(t, `
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
  - name: postgres-mcp
    transport: streamable-http
    url: http://fake2
roles:
  web-tools:
    allowed_tools:
      - "github/*"
      - "notion/*"
    credentials:
      github: GITHUB_TOKEN
  database:
    allowed_tools:
      - "db_*"
    credentials:
      postgres-mcp: POSTGRES_TOKEN
`)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "database"); err != nil {
		t.Fatal(err)
	}

	key := "sk-alice"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, roles)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)

	caller, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	// Should have access to tools from BOTH roles.
	if !caller.CanAccessTool("github/create_pr") {
		t.Error("expected github/* to match via web-tools role")
	}
	if !caller.CanAccessTool("db_query") {
		t.Error("expected db_* to match via database role")
	}
	if caller.CanAccessTool("slack/send") {
		t.Error("expected slack/send to NOT match any role")
	}

	// Should have both roles.
	if len(caller.Roles) != 2 {
		t.Errorf("expected 2 roles, got %v", caller.Roles)
	}
}

func TestSingleRole(t *testing.T) {
	store := newTestStore(t)
	roles := loadRoles(t, `
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
roles:
  web-tools:
    allowed_tools:
      - "github/*"
    credentials:
      github: GITHUB_TOKEN
`)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}

	key := "sk-alice"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, roles)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)

	caller, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if !caller.CanAccessTool("github/create_pr") {
		t.Error("expected github/* to match")
	}
	if caller.CanAccessTool("db_query") {
		t.Error("expected db_query to NOT match web-tools role")
	}
}

func TestUnknownRoleContributesNothing(t *testing.T) {
	store := newTestStore(t)
	roles := loadRoles(t, `
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
roles:
  web-tools:
    allowed_tools:
      - "github/*"
    credentials:
      github: GITHUB_TOKEN
`)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	// Assign a role not in config + a valid one.
	if err := store.AssignRole("alice", "nonexistent"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}

	key := "sk-alice"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, roles)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)

	caller, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	// Should have access from the valid web-tools role.
	if !caller.CanAccessTool("github/create_pr") {
		t.Error("expected github/* to match via web-tools role")
	}
	// Unknown role should be excluded by orderByConfig.
	if len(caller.Roles) != 1 || caller.Roles[0] != "web-tools" {
		t.Errorf("expected [web-tools], got %v", caller.Roles)
	}
}

// --- Credential injection tests ---

func TestUpstreamTokenResolves(t *testing.T) {
	t.Setenv("GITHUB_DEV_TOKEN", "ghp_dev123")

	roles := loadRoles(t, `
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
roles:
  web-tools:
    allowed_tools:
      - "github/*"
    credentials:
      github: GITHUB_DEV_TOKEN
`)

	auth := NewAuthenticator(newTestStore(t), roles)

	token, ok := auth.UpstreamToken([]string{"web-tools"}, "github")
	if !ok {
		t.Fatal("expected UpstreamToken to return true")
	}
	if token != "ghp_dev123" {
		t.Errorf("token = %q, want ghp_dev123", token)
	}
}

func TestUpstreamTokenMissingUpstream(t *testing.T) {
	t.Setenv("GITHUB_DEV_TOKEN", "ghp_dev123")

	roles := loadRoles(t, `
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
roles:
  web-tools:
    allowed_tools:
      - "github/*"
    credentials:
      github: GITHUB_DEV_TOKEN
`)

	auth := NewAuthenticator(newTestStore(t), roles)

	_, ok := auth.UpstreamToken([]string{"web-tools"}, "datadog")
	if ok {
		t.Fatal("expected false for missing upstream")
	}
}

func TestUpstreamTokenUnknownRole(t *testing.T) {
	auth := NewAuthenticator(newTestStore(t), nil)

	_, ok := auth.UpstreamToken([]string{"staging"}, "github")
	if ok {
		t.Fatal("expected false for unknown role")
	}
}

func TestCredentialInjectionUsesConfigOrder(t *testing.T) {
	t.Setenv("GITHUB_WEB_TOKEN", "ghp_web")
	t.Setenv("GITHUB_DB_TOKEN", "ghp_db")
	t.Setenv("POSTGRES_DB_TOKEN", "pg_db")

	roles := loadRoles(t, `
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
  - name: postgres-mcp
    transport: streamable-http
    url: http://fake2
roles:
  web-tools:
    allowed_tools:
      - "github/*"
    credentials:
      github: GITHUB_WEB_TOKEN
  database:
    allowed_tools:
      - "db_*"
    credentials:
      github: GITHUB_DB_TOKEN
      postgres-mcp: POSTGRES_DB_TOKEN
`)

	store := newTestStore(t)
	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "web-tools"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "database"); err != nil {
		t.Fatal(err)
	}

	key := "sk-alice"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, roles)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	caller, err := auth.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}

	// Both roles have github credentials. web-tools is first in config → wins.
	token, ok := auth.UpstreamToken(caller.Roles, "github")
	if !ok || token != "ghp_web" {
		t.Errorf("expected web-tools github token (ghp_web), got %q (ok=%v)", token, ok)
	}

	// Only database has postgres credentials → falls through.
	token, ok = auth.UpstreamToken(caller.Roles, "postgres-mcp")
	if !ok || token != "pg_db" {
		t.Errorf("expected database postgres token (pg_db), got %q (ok=%v)", token, ok)
	}
}

func TestCredentialInjectionFallsThrough(t *testing.T) {
	t.Setenv("POSTGRES_DB_TOKEN", "pg_db")

	roles := loadRoles(t, `
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
  - name: postgres-mcp
    transport: streamable-http
    url: http://fake2
roles:
  web-tools:
    allowed_tools:
      - "github/*"
    credentials:
      github: GITHUB_WEB_TOKEN
  database:
    allowed_tools:
      - "db_*"
    credentials:
      postgres-mcp: POSTGRES_DB_TOKEN
`)

	auth := NewAuthenticator(newTestStore(t), roles)

	// web-tools has no postgres credentials; database does → should fall through.
	token, ok := auth.UpstreamToken([]string{"web-tools", "database"}, "postgres-mcp")
	if !ok || token != "pg_db" {
		t.Errorf("expected fallthrough to database, got %q (ok=%v)", token, ok)
	}
}

// --- Auth middleware tests ---

func TestValidKeyAuthenticates(t *testing.T) {
	store := newTestStore(t)
	roles := loadRoles(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  admin:
    allowed_tools:
      - "*"
    credentials: {}
`)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	key := "sk-valid-key"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, roles)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)

	caller, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if caller == nil {
		t.Fatal("expected non-nil caller")
	}
	if caller.Name != "alice" {
		t.Errorf("name = %q, want alice", caller.Name)
	}
}

func TestInvalidKeyRejected(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-real-key"))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, nil)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer sk-wrong-key")

	_, err := auth.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestMissingHeaderRejected(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-key"))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, nil)

	req := httptest.NewRequest("POST", "/mcp", nil)
	_, err := auth.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for missing Authorization header")
	}
}

func TestMalformedHeaderRejected(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-key"))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, nil)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	_, err := auth.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for non-Bearer Authorization")
	}
}

func TestAuthDisabledNoCallersNoRoles(t *testing.T) {
	store := newTestStore(t)
	auth := NewAuthenticator(store, nil)

	req := httptest.NewRequest("POST", "/mcp", nil)
	caller, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("expected nil error for disabled auth, got: %v", err)
	}
	if caller != nil {
		t.Fatal("expected nil caller for disabled auth")
	}
}

// --- Middleware integration tests ---

func TestMiddlewareSetsCaller(t *testing.T) {
	store := newTestStore(t)
	roles := loadRoles(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  admin:
    allowed_tools:
      - "*"
    credentials: {}
`)

	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	key := "sk-middleware-test"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, roles)

	var gotCaller *Caller
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCaller = CallerFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := auth.Middleware(inner)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if gotCaller == nil {
		t.Fatal("expected caller in context")
	}
	if gotCaller.Name != "alice" {
		t.Errorf("caller name = %q, want alice", gotCaller.Name)
	}
}

func TestMiddlewareRejectsInvalidKey(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-real"))
	if err := store.AddKey("alice", hash, ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, nil)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called")
	})

	handler := auth.Middleware(inner)

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer sk-wrong")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		// JSON-RPC errors are written as 200 with error body
		t.Logf("status = %d (JSON-RPC errors use 200)", w.Code)
	}
	body := w.Body.String()
	if body == "" {
		t.Fatal("expected error response body")
	}
}

func TestMiddlewarePassesThroughWhenDisabled(t *testing.T) {
	store := newTestStore(t)
	auth := NewAuthenticator(store, nil)

	var gotCaller *Caller
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCaller = CallerFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := auth.Middleware(inner)

	req := httptest.NewRequest("POST", "/mcp", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if gotCaller != nil {
		t.Error("expected nil caller when auth is disabled")
	}
}

// --- Admin auth tests ---

func TestAdminValidKeyAccepted(t *testing.T) {
	adminKey := "admin-secret"
	adminHash := sha256.Sum256([]byte(adminKey))
	store := newTestStore(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AdminAuthMiddleware(adminHash, store)(inner)

	req := httptest.NewRequest("POST", "/admin/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAdminInvalidKeyRejected(t *testing.T) {
	adminHash := sha256.Sum256([]byte("admin-secret"))
	store := newTestStore(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner should not be called")
	})

	handler := AdminAuthMiddleware(adminHash, store)(inner)

	req := httptest.NewRequest("POST", "/admin/refresh", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestAdminDevModeNoKeyNoCallers(t *testing.T) {
	var zeroHash [32]byte
	store := newTestStore(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AdminAuthMiddleware(zeroHash, store)(inner)

	req := httptest.NewRequest("POST", "/admin/refresh", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 in dev mode, got %d", w.Code)
	}
}

func TestAdminNoKeyButCallersExist(t *testing.T) {
	var zeroHash [32]byte
	store := newTestStore(t)
	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner should not be called")
	})

	handler := AdminAuthMiddleware(zeroHash, store)(inner)

	req := httptest.NewRequest("POST", "/admin/refresh", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 when callers exist but no admin key, got %d", w.Code)
	}
}

// --- CallerFromContext tests ---

func TestCallerFromContextNil(t *testing.T) {
	ctx := context.Background()
	if c := CallerFromContext(ctx); c != nil {
		t.Error("expected nil from empty context")
	}
}

func TestCallerFromContextSet(t *testing.T) {
	caller := &Caller{Name: "test"}
	ctx := contextWithCaller(context.Background(), caller)
	got := CallerFromContext(ctx)
	if got == nil || got.Name != "test" {
		t.Error("expected caller from context")
	}
}
