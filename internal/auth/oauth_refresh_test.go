package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anguslmm/stile/internal/config"
)

func TestTokenRefresher_Refresh(t *testing.T) {
	// Mock token endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", 400)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" {
			http.Error(w, "expected refresh_token grant type", 400)
			return
		}
		if r.FormValue("refresh_token") != "old-refresh" {
			http.Error(w, "wrong refresh token", 400)
			return
		}
		if r.FormValue("client_id") != "cid" || r.FormValue("client_secret") != "csec" {
			http.Error(w, "wrong credentials", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"token_type":    "Bearer",
			"refresh_token": "new-refresh",
			"expires_in":    7200,
		})
	}))
	defer srv.Close()

	t.Setenv("REFRESH_TEST_ID", "cid")
	t.Setenv("REFRESH_TEST_SECRET", "csec")

	yamlStr := `
oauth_providers:
  testprovider:
    auth_url: ` + srv.URL + `/authorize
    token_url: ` + srv.URL + `/token
    client_id_env: REFRESH_TEST_ID
    client_secret_env: REFRESH_TEST_SECRET
    scopes: ["read"]

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    auth:
      type: oauth
      provider: testprovider
`
	cfg, err := config.LoadBytes([]byte(yamlStr))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	refresher := NewTokenRefresher(cfg.OAuthProviders(), nil)

	token, err := refresher.Refresh(context.Background(), "testprovider", "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if token.AccessToken != "new-access" {
		t.Errorf("access_token = %q, want new-access", token.AccessToken)
	}
	if token.RefreshToken != "new-refresh" {
		t.Errorf("refresh_token = %q, want new-refresh", token.RefreshToken)
	}
	if token.Expiry.IsZero() {
		t.Error("expected non-zero expiry")
	}
}

func TestTokenRefresher_KeepsOldRefreshToken(t *testing.T) {
	// Provider doesn't return a new refresh token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	t.Setenv("KRT_ID", "cid")
	t.Setenv("KRT_SECRET", "csec")

	yamlStr := `
oauth_providers:
  testprovider:
    auth_url: ` + srv.URL + `/authorize
    token_url: ` + srv.URL + `/token
    client_id_env: KRT_ID
    client_secret_env: KRT_SECRET

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    auth:
      type: oauth
      provider: testprovider
`
	cfg, err := config.LoadBytes([]byte(yamlStr))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	refresher := NewTokenRefresher(cfg.OAuthProviders(), nil)
	token, err := refresher.Refresh(context.Background(), "testprovider", "keep-this-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if token.RefreshToken != "keep-this-refresh" {
		t.Errorf("expected old refresh token to be kept, got %q", token.RefreshToken)
	}
}

func TestTokenRefresher_UnknownProvider(t *testing.T) {
	refresher := NewTokenRefresher(nil, nil)
	_, err := refresher.Refresh(context.Background(), "nonexistent", "token")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestTokenRefresher_EmptyRefreshToken(t *testing.T) {
	t.Setenv("ERT_ID", "cid")
	t.Setenv("ERT_SECRET", "csec")

	yamlStr := `
oauth_providers:
  testprovider:
    auth_url: https://example.com/authorize
    token_url: https://example.com/token
    client_id_env: ERT_ID
    client_secret_env: ERT_SECRET

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    auth:
      type: oauth
      provider: testprovider
`
	cfg, err := config.LoadBytes([]byte(yamlStr))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	refresher := NewTokenRefresher(cfg.OAuthProviders(), nil)
	_, err = refresher.Refresh(context.Background(), "testprovider", "")
	if err == nil {
		t.Fatal("expected error for empty refresh token")
	}
}
