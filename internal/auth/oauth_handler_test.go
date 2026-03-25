package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/config"
)

func newTestOAuthProvider(t *testing.T) (*httptest.Server, config.OAuthProviderConfig) {
	t.Helper()

	// Mock OAuth provider that accepts authorization codes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/token"):
			// Token endpoint.
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			code := r.FormValue("code")
			if code == "bad-code" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			// Verify PKCE code_verifier is present.
			verifier := r.FormValue("code_verifier")
			if verifier == "" {
				http.Error(w, "missing code_verifier", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "test-access-token",
				"token_type":    "Bearer",
				"refresh_token": "test-refresh-token",
				"expires_in":    3600,
				"scope":         "repo",
			})
		default:
			http.NotFound(w, r)
		}
	}))

	t.Setenv("TEST_OAUTH_CLIENT_ID", "test-client-id")
	t.Setenv("TEST_OAUTH_CLIENT_SECRET", "test-client-secret")

	// Build a config (we need the raw fields, so use LoadBytes to parse a minimal config).
	yamlStr := fmt.Sprintf(`
oauth_providers:
  testprovider:
    auth_url: %s/authorize
    token_url: %s/token
    client_id_env: TEST_OAUTH_CLIENT_ID
    client_secret_env: TEST_OAUTH_CLIENT_SECRET
    scopes: ["repo"]

upstreams:
  - name: test-upstream
    transport: streamable-http
    url: https://test-upstream.example.com
    auth:
      type: oauth
      provider: testprovider
`, srv.URL, srv.URL)

	cfg, err := config.LoadBytes([]byte(yamlStr))
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}

	providers := cfg.OAuthProviders()
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}

	return srv, providers[0]
}

func TestOAuthHandler_ConnectFlow(t *testing.T) {
	providerSrv, providerCfg := newTestOAuthProvider(t)
	defer providerSrv.Close()

	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	stileBase := "https://stile.example.com"
	handler := NewOAuthHandler(
		[]config.OAuthProviderConfig{providerCfg},
		tokenStore,
		stileBase,
	)

	mux := http.NewServeMux()
	handler.Register(mux)

	// Step 1: Start the flow (must be authenticated).
	req := httptest.NewRequest("GET", "/oauth/connect/testprovider", nil)
	ctx := ContextWithCaller(req.Context(), &Caller{Name: "alice@example.com"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header")
	}

	locURL, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse location URL: %v", err)
	}

	// Verify redirect URL parameters.
	if locURL.Query().Get("client_id") != "test-client-id" {
		t.Errorf("client_id = %q", locURL.Query().Get("client_id"))
	}
	if locURL.Query().Get("response_type") != "code" {
		t.Errorf("response_type = %q", locURL.Query().Get("response_type"))
	}
	if locURL.Query().Get("redirect_uri") != stileBase+"/oauth/callback" {
		t.Errorf("redirect_uri = %q", locURL.Query().Get("redirect_uri"))
	}
	state := locURL.Query().Get("state")
	if state == "" {
		t.Fatal("expected state parameter")
	}
	if locURL.Query().Get("code_challenge") == "" {
		t.Fatal("expected code_challenge (PKCE)")
	}
	if locURL.Query().Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", locURL.Query().Get("code_challenge_method"))
	}
	if locURL.Query().Get("scope") != "repo" {
		t.Errorf("scope = %q, want repo", locURL.Query().Get("scope"))
	}

	// Step 2: Simulate callback from provider.
	callbackURL := fmt.Sprintf("/oauth/callback?state=%s&code=valid-code", state)
	req2 := httptest.NewRequest("GET", callbackURL, nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("callback returned %d: %s", w2.Code, w2.Body.String())
	}

	// Verify token was stored.
	token, err := tokenStore.GetToken(context.Background(), "alice@example.com", "testprovider")
	if err != nil {
		t.Fatalf("GetToken after callback: %v", err)
	}
	if token.AccessToken != "test-access-token" {
		t.Errorf("access_token = %q, want test-access-token", token.AccessToken)
	}
	if token.RefreshToken != "test-refresh-token" {
		t.Errorf("refresh_token = %q, want test-refresh-token", token.RefreshToken)
	}
}

func TestOAuthHandler_ConnectRequiresAuth(t *testing.T) {
	providerSrv, providerCfg := newTestOAuthProvider(t)
	defer providerSrv.Close()

	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	handler := NewOAuthHandler(
		[]config.OAuthProviderConfig{providerCfg},
		tokenStore,
		"https://stile.example.com",
	)

	mux := http.NewServeMux()
	handler.Register(mux)

	// No caller in context → unauthorized.
	req := httptest.NewRequest("GET", "/oauth/connect/testprovider", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestOAuthHandler_UnknownProvider(t *testing.T) {
	providerSrv, providerCfg := newTestOAuthProvider(t)
	defer providerSrv.Close()

	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	handler := NewOAuthHandler(
		[]config.OAuthProviderConfig{providerCfg},
		tokenStore,
		"https://stile.example.com",
	)

	mux := http.NewServeMux()
	handler.Register(mux)

	req := httptest.NewRequest("GET", "/oauth/connect/nonexistent", nil)
	ctx := ContextWithCaller(req.Context(), &Caller{Name: "alice"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestOAuthHandler_InvalidState(t *testing.T) {
	providerSrv, providerCfg := newTestOAuthProvider(t)
	defer providerSrv.Close()

	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	handler := NewOAuthHandler(
		[]config.OAuthProviderConfig{providerCfg},
		tokenStore,
		"https://stile.example.com",
	)

	mux := http.NewServeMux()
	handler.Register(mux)

	// Callback with unknown state.
	req := httptest.NewRequest("GET", "/oauth/callback?state=bogus&code=whatever", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestOAuthHandler_ProviderError(t *testing.T) {
	providerSrv, providerCfg := newTestOAuthProvider(t)
	defer providerSrv.Close()

	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	handler := NewOAuthHandler(
		[]config.OAuthProviderConfig{providerCfg},
		tokenStore,
		"https://stile.example.com",
	)

	mux := http.NewServeMux()
	handler.Register(mux)

	// Provider returns error in callback.
	req := httptest.NewRequest("GET", "/oauth/callback?error=access_denied&error_description=user+denied", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestOAuthHandler_BadCodeExchange(t *testing.T) {
	providerSrv, providerCfg := newTestOAuthProvider(t)
	defer providerSrv.Close()

	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	handler := NewOAuthHandler(
		[]config.OAuthProviderConfig{providerCfg},
		tokenStore,
		"https://stile.example.com",
	)

	mux := http.NewServeMux()
	handler.Register(mux)

	// Start a flow to get a valid state.
	req := httptest.NewRequest("GET", "/oauth/connect/testprovider", nil)
	ctx := ContextWithCaller(req.Context(), &Caller{Name: "alice"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	loc := w.Header().Get("Location")
	locURL, _ := url.Parse(loc)
	state := locURL.Query().Get("state")

	// Use a bad code.
	callbackURL := fmt.Sprintf("/oauth/callback?state=%s&code=bad-code", state)
	req2 := httptest.NewRequest("GET", callbackURL, nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", w2.Code, w2.Body.String())
	}
}
