package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/anguslmm/stile/internal/config"
)

// --- Fake OIDC provider ---

type fakeOIDCProvider struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	issuer     string

	// userinfoHandler can be overridden per-test.
	userinfoMu      sync.Mutex
	userinfoHandler func(w http.ResponseWriter, r *http.Request)
}

func newFakeOIDCProvider(t *testing.T) *fakeOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	p := &fakeOIDCProvider{privateKey: key}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.handleDiscovery)
	mux.HandleFunc("GET /keys", p.handleJWKS)
	mux.HandleFunc("GET /userinfo", p.handleUserinfo)
	p.server = httptest.NewServer(mux)
	p.issuer = p.server.URL

	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeOIDCProvider) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"issuer":                                p.issuer,
		"jwks_uri":                              p.issuer + "/keys",
		"userinfo_endpoint":                     p.issuer + "/userinfo",
		"authorization_endpoint":                p.issuer + "/auth",
		"token_endpoint":                        p.issuer + "/token",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *fakeOIDCProvider) handleJWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jwks := jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       &p.privateKey.PublicKey,
			KeyID:     "test-key-1",
			Algorithm: "RS256",
			Use:       "sig",
		}},
	}
	json.NewEncoder(w).Encode(jwks)
}

func (p *fakeOIDCProvider) handleUserinfo(w http.ResponseWriter, r *http.Request) {
	p.userinfoMu.Lock()
	handler := p.userinfoHandler
	p.userinfoMu.Unlock()
	if handler != nil {
		handler(w, r)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
}

func (p *fakeOIDCProvider) setUserinfoHandler(fn func(w http.ResponseWriter, r *http.Request)) {
	p.userinfoMu.Lock()
	defer p.userinfoMu.Unlock()
	p.userinfoHandler = fn
}

func (p *fakeOIDCProvider) signToken(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: p.privateKey},
		(&jose.SignerOptions{}).WithHeader("kid", "test-key-1").WithType("JWT"),
	)
	if err != nil {
		t.Fatal(err)
	}

	builder := josejwt.Signed(signer).Claims(claims)
	raw, err := builder.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// --- Config helpers ---

func newOIDCConfig(issuer, audience, validation string) *config.OIDCConfig {
	yaml := "upstreams:\n  - name: svc\n    transport: streamable-http\n    url: http://fake\nroles:\n  dev:\n    allowed_tools: [\"*\"]\nauth:\n  oidc:\n    issuer: " + issuer + "\n"
	if audience != "" {
		yaml += "    audience: " + audience + "\n"
	}
	if validation != "" {
		yaml += "    validation: " + validation + "\n"
	}
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		panic(err)
	}
	return cfg.OIDC()
}

func newOIDCConfigFull(issuer string, opts map[string]interface{}) *config.OIDCConfig {
	yaml := "upstreams:\n  - name: svc\n    transport: streamable-http\n    url: http://fake\nroles:\n  dev:\n    allowed_tools: [\"*\"]\nauth:\n  oidc:\n    issuer: " + issuer + "\n"
	if v, ok := opts["audience"]; ok {
		yaml += "    audience: " + v.(string) + "\n"
	}
	if v, ok := opts["validation"]; ok {
		yaml += "    validation: " + v.(string) + "\n"
	}
	if v, ok := opts["caller_claim"]; ok {
		yaml += "    caller_claim: " + v.(string) + "\n"
	}
	if v, ok := opts["auto_provision"]; ok && v.(bool) {
		yaml += "    auto_provision: true\n"
	}
	if v, ok := opts["default_roles"]; ok {
		yaml += "    default_roles:\n"
		for _, r := range v.([]string) {
			yaml += "      - " + r + "\n"
		}
	}
	if v, ok := opts["allowed_domains"]; ok {
		yaml += "    allowed_domains:\n"
		for _, d := range v.([]string) {
			yaml += "      - " + d + "\n"
		}
	}
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		panic(err)
	}
	return cfg.OIDC()
}

// --- JWT validation tests ---

func TestOIDCJWTValid(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	oidcCfg := newOIDCConfig(provider.issuer, "test-client", "jwt")

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"sub":   "user-1",
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	identity, err := v.Validate(context.Background(), token)
	if err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
	if identity != "alice@example.com" {
		t.Errorf("identity = %q, want alice@example.com", identity)
	}
}

func TestOIDCJWTExpired(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	oidcCfg := newOIDCConfig(provider.issuer, "test-client", "jwt")

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"email": "alice@example.com",
		"exp":   time.Now().Add(-time.Hour).Unix(),
		"iat":   time.Now().Add(-2 * time.Hour).Unix(),
	})

	_, err = v.Validate(context.Background(), token)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestOIDCJWTWrongAudience(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	oidcCfg := newOIDCConfig(provider.issuer, "test-client", "jwt")

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "wrong-client",
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	_, err = v.Validate(context.Background(), token)
	if err == nil {
		t.Fatal("expected error for wrong audience")
	}
}

func TestOIDCJWTMissingCallerClaim(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	oidcCfg := newOIDCConfig(provider.issuer, "test-client", "jwt")

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	token := provider.signToken(t, map[string]interface{}{
		"iss": provider.issuer,
		"aud": "test-client",
		"sub": "user-1",
		// no "email" claim
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	})

	_, err = v.Validate(context.Background(), token)
	if err == nil {
		t.Fatal("expected error for missing caller claim")
	}
}

func TestOIDCJWTCustomCallerClaim(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	oidcCfg := newOIDCConfigFull(provider.issuer, map[string]interface{}{
		"audience":     "test-client",
		"validation":   "jwt",
		"caller_claim": "sub",
	})

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"sub":   "user-123",
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	identity, err := v.Validate(context.Background(), token)
	if err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
	if identity != "user-123" {
		t.Errorf("identity = %q, want user-123", identity)
	}
}

// --- Domain filtering tests ---

func TestOIDCJWTDomainAllowed(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	oidcCfg := newOIDCConfigFull(provider.issuer, map[string]interface{}{
		"audience":        "test-client",
		"validation":      "jwt",
		"allowed_domains": []string{"example.com"},
	})

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	identity, err := v.Validate(context.Background(), token)
	if err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
	if identity != "alice@example.com" {
		t.Errorf("identity = %q, want alice@example.com", identity)
	}
}

func TestOIDCJWTDomainRejected(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	oidcCfg := newOIDCConfigFull(provider.issuer, map[string]interface{}{
		"audience":        "test-client",
		"validation":      "jwt",
		"allowed_domains": []string{"example.com"},
	})

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"email": "mallory@evil.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	_, err = v.Validate(context.Background(), token)
	if err == nil {
		t.Fatal("expected error for domain not in allowed list")
	}
}

// --- Userinfo validation tests ---

func TestOIDCUserinfoValid(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	provider.setUserinfoHandler(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-opaque-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sub":   "user-123",
			"email": "alice@example.com",
		})
	})

	oidcCfg := newOIDCConfigFull(provider.issuer, map[string]interface{}{
		"validation": "userinfo",
	})

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	identity, err := v.Validate(context.Background(), "valid-opaque-token")
	if err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
	if identity != "alice@example.com" {
		t.Errorf("identity = %q, want alice@example.com", identity)
	}
}

func TestOIDCUserinfoInvalidToken(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	// Default handler returns 401 for unknown tokens.

	oidcCfg := newOIDCConfigFull(provider.issuer, map[string]interface{}{
		"validation": "userinfo",
	})

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	_, err = v.Validate(context.Background(), "invalid-token")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestOIDCUserinfoCaching(t *testing.T) {
	callCount := 0
	provider := newFakeOIDCProvider(t)
	provider.setUserinfoHandler(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"email": "alice@example.com",
		})
	})

	oidcCfg := newOIDCConfigFull(provider.issuer, map[string]interface{}{
		"validation": "userinfo",
	})

	v, err := NewOIDCValidator(context.Background(), oidcCfg, WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	// First call: hits endpoint.
	if _, err := v.Validate(context.Background(), "token-1"); err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}

	// Second call with same token: should hit cache.
	if _, err := v.Validate(context.Background(), "token-1"); err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Errorf("expected still 1 call after cache hit, got %d", callCount)
	}

	// Different token: new call.
	if _, err := v.Validate(context.Background(), "token-2"); err != nil {
		t.Fatal(err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

// --- Authenticator OIDC integration tests ---

func TestAuthenticatorOIDCJWT(t *testing.T) {
	provider := newFakeOIDCProvider(t)

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  dev:
    allowed_tools: ["*"]
auth:
  oidc:
    issuer: ` + provider.issuer + `
    audience: test-client
    validation: jwt
    auto_provision: true
    default_roles: ["dev"]
`))
	if err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t)
	oidcValidator, err := NewOIDCValidator(context.Background(), cfg.OIDC(), WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	authenticator := NewAuthenticator(store, cfg.Roles(), WithOIDCValidator(oidcValidator, cfg.OIDC()))

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	caller, keyLabel, err := authenticator.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if caller.Name != "alice@example.com" {
		t.Errorf("caller = %q, want alice@example.com", caller.Name)
	}
	if keyLabel != "oidc" {
		t.Errorf("keyLabel = %q, want oidc", keyLabel)
	}

	// Verify auto-provisioning created the caller with default roles.
	exists, err := store.CallerExists("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected caller to be auto-provisioned")
	}
	roles, _ := store.RolesForCaller("alice@example.com")
	if len(roles) != 1 || roles[0] != "dev" {
		t.Errorf("expected [dev] roles, got %v", roles)
	}
}

func TestAuthenticatorOIDCFallbackToAPIKey(t *testing.T) {
	provider := newFakeOIDCProvider(t)

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  dev:
    allowed_tools: ["*"]
auth:
  oidc:
    issuer: ` + provider.issuer + `
    audience: test-client
    validation: jwt
`))
	if err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t)
	if err := store.AddCaller("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole("alice", "dev"); err != nil {
		t.Fatal(err)
	}
	key := "sk-alice-key"
	hash := sha256.Sum256([]byte(key))
	if err := store.AddKey("alice", hash, "dev-key"); err != nil {
		t.Fatal(err)
	}

	oidcValidator, err := NewOIDCValidator(context.Background(), cfg.OIDC(), WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	authenticator := NewAuthenticator(store, cfg.Roles(), WithOIDCValidator(oidcValidator, cfg.OIDC()))

	// API key auth should still work even with OIDC configured.
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)

	caller, keyLabel, err := authenticator.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if caller.Name != "alice" {
		t.Errorf("caller = %q, want alice", caller.Name)
	}
	if keyLabel != "dev-key" {
		t.Errorf("keyLabel = %q, want dev-key", keyLabel)
	}
}

func TestAuthenticatorOIDCRejectsUnregistered(t *testing.T) {
	provider := newFakeOIDCProvider(t)

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  dev:
    allowed_tools: ["*"]
auth:
  oidc:
    issuer: ` + provider.issuer + `
    audience: test-client
    validation: jwt
    auto_provision: false
`))
	if err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t)
	oidcValidator, err := NewOIDCValidator(context.Background(), cfg.OIDC(), WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	authenticator := NewAuthenticator(store, cfg.Roles(), WithOIDCValidator(oidcValidator, cfg.OIDC()))

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"email": "unknown@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, _, err = authenticator.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for unregistered caller with auto_provision=false")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Logf("error: %v (OIDC rejection falls through to API key)", err)
	}
}

func TestAuthenticatorOIDCAutoProvisionIdempotent(t *testing.T) {
	provider := newFakeOIDCProvider(t)

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  dev:
    allowed_tools: ["*"]
auth:
  oidc:
    issuer: ` + provider.issuer + `
    audience: test-client
    validation: jwt
    auto_provision: true
    default_roles: ["dev"]
`))
	if err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t)
	oidcValidator, err := NewOIDCValidator(context.Background(), cfg.OIDC(), WithOIDCHTTPClient(provider.server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	authenticator := NewAuthenticator(store, cfg.Roles(), WithOIDCValidator(oidcValidator, cfg.OIDC()))

	token := provider.signToken(t, map[string]interface{}{
		"iss":   provider.issuer,
		"aud":   "test-client",
		"email": "bob@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	// First auth: creates caller.
	if _, _, err := authenticator.Authenticate(req); err != nil {
		t.Fatalf("first auth failed: %v", err)
	}

	// Second auth: should succeed (idempotent provisioning).
	caller, _, err := authenticator.Authenticate(req)
	if err != nil {
		t.Fatalf("second auth failed: %v", err)
	}
	if caller.Name != "bob@example.com" {
		t.Errorf("caller = %q, want bob@example.com", caller.Name)
	}

	// Roles should not be duplicated.
	roles, _ := store.RolesForCaller("bob@example.com")
	if len(roles) != 1 {
		t.Errorf("expected 1 role, got %d: %v", len(roles), roles)
	}
}

// --- isJWT tests ---

func TestIsJWT(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJ0ZXN0In0.sig", true},
		{"a.b.c", true},
		{"sk-abcdef1234567890", false},
		{"opaque-token", false},
		{"a.b", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isJWT(tc.token); got != tc.want {
			t.Errorf("isJWT(%q) = %v, want %v", tc.token, got, tc.want)
		}
	}
}

// --- EnsureCaller tests ---

func TestEnsureCallerCreatesNew(t *testing.T) {
	store := newTestStore(t)

	if err := store.EnsureCaller("alice@example.com", []string{"dev", "admin"}); err != nil {
		t.Fatal(err)
	}

	exists, err := store.CallerExists("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected caller to exist")
	}

	roles, _ := store.RolesForCaller("alice@example.com")
	if len(roles) != 2 {
		t.Fatalf("expected 2 roles, got %v", roles)
	}
}

func TestEnsureCallerIdempotent(t *testing.T) {
	store := newTestStore(t)

	// First call creates.
	if err := store.EnsureCaller("alice@example.com", []string{"dev"}); err != nil {
		t.Fatal(err)
	}

	// Second call is a no-op.
	if err := store.EnsureCaller("alice@example.com", []string{"admin"}); err != nil {
		t.Fatal(err)
	}

	// Should still have only the original role.
	roles, _ := store.RolesForCaller("alice@example.com")
	if len(roles) != 1 || roles[0] != "dev" {
		t.Errorf("expected [dev] (original), got %v", roles)
	}
}

func TestCallerExistsNonexistent(t *testing.T) {
	store := newTestStore(t)

	exists, err := store.CallerExists("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("expected false for nonexistent caller")
	}
}

// --- Config parsing tests ---

func TestOIDCConfigParsing(t *testing.T) {
	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  dev:
    allowed_tools: ["*"]
auth:
  oidc:
    issuer: https://accounts.google.com
    audience: my-client-id
    caller_claim: email
    validation: userinfo
    auto_provision: true
    default_roles: ["developer"]
    allowed_domains: ["example.com", "corp.com"]
`))
	if err != nil {
		t.Fatal(err)
	}

	oidc := cfg.OIDC()
	if oidc == nil {
		t.Fatal("expected OIDC config")
	}
	if oidc.Issuer() != "https://accounts.google.com" {
		t.Errorf("issuer = %q", oidc.Issuer())
	}
	if oidc.Audience() != "my-client-id" {
		t.Errorf("audience = %q", oidc.Audience())
	}
	if oidc.CallerClaim() != "email" {
		t.Errorf("caller_claim = %q", oidc.CallerClaim())
	}
	if oidc.Validation() != "userinfo" {
		t.Errorf("validation = %q", oidc.Validation())
	}
	if !oidc.AutoProvision() {
		t.Error("expected auto_provision = true")
	}
	if dr := oidc.DefaultRoles(); len(dr) != 1 || dr[0] != "developer" {
		t.Errorf("default_roles = %v", dr)
	}
	if ad := oidc.AllowedDomains(); len(ad) != 2 || ad[0] != "example.com" {
		t.Errorf("allowed_domains = %v", ad)
	}
}

func TestOIDCConfigDefaults(t *testing.T) {
	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  dev:
    allowed_tools: ["*"]
auth:
  oidc:
    issuer: https://accounts.google.com
    audience: my-client-id
`))
	if err != nil {
		t.Fatal(err)
	}

	oidc := cfg.OIDC()
	if oidc.Validation() != "jwt" {
		t.Errorf("expected default validation=jwt, got %q", oidc.Validation())
	}
	if oidc.CallerClaim() != "email" {
		t.Errorf("expected default caller_claim=email, got %q", oidc.CallerClaim())
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		err  string
	}{
		{
			"missing issuer",
			`auth:
  oidc:
    audience: foo`,
			"auth.oidc.issuer is required",
		},
		{
			"invalid validation mode",
			`auth:
  oidc:
    issuer: https://example.com
    validation: magic`,
			"auth.oidc.validation must be",
		},
		{
			"jwt mode missing audience",
			`auth:
  oidc:
    issuer: https://example.com
    validation: jwt`,
			"auth.oidc.audience is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			full := "upstreams:\n  - name: svc\n    transport: streamable-http\n    url: http://fake\nroles:\n  dev:\n    allowed_tools: [\"*\"]\n" + tc.yaml
			_, err := config.LoadBytes([]byte(full))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.err) {
				t.Errorf("error = %q, want substring %q", err, tc.err)
			}
		})
	}
}

func TestOIDCNilWhenNotConfigured(t *testing.T) {
	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  dev:
    allowed_tools: ["*"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC() != nil {
		t.Error("expected nil OIDC config when not configured")
	}
}

// helper for tests that need a store with a specific DB path
func newTestStoreAt(t *testing.T, dir string) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(dir, "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
