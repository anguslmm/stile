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

// --- CallerStore tests ---

func TestCreateAndLookupCaller(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice", []string{"github/*", "notion/*"}); err != nil {
		t.Fatal(err)
	}

	key := "sk-test-key-alice"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, "dev", "alice-dev"); err != nil {
		t.Fatal(err)
	}

	caller, err := store.LookupByKey(hash)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Name != "alice" {
		t.Errorf("name = %q, want alice", caller.Name)
	}
	if caller.AuthEnv != "dev" {
		t.Errorf("auth_env = %q, want dev", caller.AuthEnv)
	}
	if len(caller.AllowedTools) != 2 {
		t.Errorf("expected 2 allowed_tools patterns, got %d", len(caller.AllowedTools))
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

	if err := store.AddCaller("bob", []string{"*"}); err != nil {
		t.Fatal(err)
	}

	devKey := "sk-bob-dev"
	devHash := sha256.Sum256([]byte(devKey))
	if err := store.AddKey("bob", devHash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	prodKey := "sk-bob-prod"
	prodHash := sha256.Sum256([]byte(prodKey))
	if err := store.AddKey("bob", prodHash, "prod", ""); err != nil {
		t.Fatal(err)
	}

	caller, err := store.LookupByKey(devHash)
	if err != nil {
		t.Fatal(err)
	}
	if caller.AuthEnv != "dev" {
		t.Errorf("dev key auth_env = %q, want dev", caller.AuthEnv)
	}

	caller, err = store.LookupByKey(prodHash)
	if err != nil {
		t.Fatal(err)
	}
	if caller.AuthEnv != "prod" {
		t.Errorf("prod key auth_env = %q, want prod", caller.AuthEnv)
	}
}

func TestMultipleCallers(t *testing.T) {
	store := newTestStore(t)

	if err := store.AddCaller("alice", []string{"github/*"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCaller("bob", []string{"notion/*"}); err != nil {
		t.Fatal(err)
	}

	aliceHash := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", aliceHash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	bobHash := sha256.Sum256([]byte("sk-bob"))
	if err := store.AddKey("bob", bobHash, "prod", ""); err != nil {
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

	if err := store.AddCaller("charlie", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-charlie"))
	if err := store.AddKey("charlie", hash, "dev", ""); err != nil {
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

func TestHasCallers(t *testing.T) {
	store := newTestStore(t)

	has, err := store.HasCallers()
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("expected no callers in fresh database")
	}

	if err := store.AddCaller("alice", []string{"*"}); err != nil {
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

// --- Access control tests ---

func TestCanAccessToolExactMatch(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice", []string{"db_query"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	caller, _ := store.LookupByKey(hash)
	if !caller.CanAccessTool("db_query") {
		t.Error("expected exact match to succeed")
	}
}

func TestCanAccessToolGlobMatch(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice", []string{"github/*"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	caller, _ := store.LookupByKey(hash)
	if !caller.CanAccessTool("github/create_pr") {
		t.Error("expected glob github/* to match github/create_pr")
	}
}

func TestCanAccessToolGlobReject(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice", []string{"github/*"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	caller, _ := store.LookupByKey(hash)
	if caller.CanAccessTool("linear/list_issues") {
		t.Error("expected github/* to NOT match linear/list_issues")
	}
}

func TestCanAccessToolMultiplePatterns(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice", []string{"github/*", "db_query"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-alice"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	caller, _ := store.LookupByKey(hash)
	if !caller.CanAccessTool("github/create_pr") {
		t.Error("expected github/* to match")
	}
	if !caller.CanAccessTool("db_query") {
		t.Error("expected db_query exact match")
	}
	if caller.CanAccessTool("slack/send") {
		t.Error("expected slack/send to NOT match")
	}
}

// --- Auth middleware tests ---

func TestValidKeyAuthenticates(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	key := "sk-valid-key"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, nil)

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
	if err := store.AddCaller("alice", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-real-key"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
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
	if err := store.AddCaller("alice", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-key"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
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
	if err := store.AddCaller("alice", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-key"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
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

func TestAuthDisabledNoCallersNoEnvs(t *testing.T) {
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

// --- Credential injection tests ---

func TestUpstreamTokenResolves(t *testing.T) {
	t.Setenv("GITHUB_DEV_TOKEN", "ghp_dev123")

	envs := []config.AuthEnvConfig{}
	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
auth_envs:
  dev:
    github: GITHUB_DEV_TOKEN
`))
	if err != nil {
		t.Fatal(err)
	}
	envs = cfg.AuthEnvs()

	auth := NewAuthenticator(newTestStore(t), envs)

	token, ok := auth.UpstreamToken("dev", "github")
	if !ok {
		t.Fatal("expected UpstreamToken to return true")
	}
	if token != "ghp_dev123" {
		t.Errorf("token = %q, want ghp_dev123", token)
	}
}

func TestUpstreamTokenMissingUpstream(t *testing.T) {
	t.Setenv("GITHUB_DEV_TOKEN", "ghp_dev123")

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: github
    transport: streamable-http
    url: http://fake
auth_envs:
  dev:
    github: GITHUB_DEV_TOKEN
`))
	if err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(newTestStore(t), cfg.AuthEnvs())

	_, ok := auth.UpstreamToken("dev", "datadog")
	if ok {
		t.Fatal("expected false for missing upstream")
	}
}

func TestUpstreamTokenUnknownEnv(t *testing.T) {
	auth := NewAuthenticator(newTestStore(t), nil)

	_, ok := auth.UpstreamToken("staging", "github")
	if ok {
		t.Fatal("expected false for unknown env")
	}
}

// --- Middleware integration tests ---

func TestMiddlewareSetsCaller(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddCaller("alice", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	key := "sk-middleware-test"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthenticator(store, nil)

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
	if err := store.AddCaller("alice", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("sk-real"))
	if err := store.AddKey("alice", hash, "dev", ""); err != nil {
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
	if err := store.AddCaller("alice", []string{"*"}); err != nil {
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
