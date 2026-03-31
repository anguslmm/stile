package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/config"
)

func TestOAuthResolver_IsConnected(t *testing.T) {
	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	yamlStr := `
oauth_providers:
  github:
    auth_url: https://github.com/login/oauth/authorize
    token_url: https://github.com/login/oauth/access_token
    client_id_env: GITHUB_CLIENT_ID
    client_secret_env: GITHUB_CLIENT_SECRET

upstreams:
  - name: github-tools
    transport: streamable-http
    url: https://github-upstream.example.com
    auth:
      type: oauth
      provider: github
  - name: plain-upstream
    transport: streamable-http
    url: https://plain.example.com
`
	cfg, err := config.LoadBytes([]byte(yamlStr))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	resolver := NewOAuthResolver(cfg.Upstreams(), tokenStore, nil)

	ctx := context.Background()

	// Non-OAuth upstream is always connected.
	ok, prov := resolver.IsConnected(ctx, "alice", "plain-upstream")
	if !ok || prov != "" {
		t.Errorf("plain upstream: connected=%v, provider=%q; want true, \"\"", ok, prov)
	}

	// OAuth upstream without token → not connected.
	ok, prov = resolver.IsConnected(ctx, "alice", "github-tools")
	if ok || prov != "github" {
		t.Errorf("unconnected oauth: connected=%v, provider=%q; want false, \"github\"", ok, prov)
	}

	// Store a token → now connected.
	err = tokenStore.StoreToken(ctx, "alice", "github", &OAuthToken{AccessToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	ok, prov = resolver.IsConnected(ctx, "alice", "github-tools")
	if !ok || prov != "" {
		t.Errorf("connected oauth: connected=%v, provider=%q; want true, \"\"", ok, prov)
	}

	// Different user still not connected.
	ok, prov = resolver.IsConnected(ctx, "bob", "github-tools")
	if ok || prov != "github" {
		t.Errorf("bob unconnected: connected=%v, provider=%q; want false, \"github\"", ok, prov)
	}
}

func TestConnectionCheckerAdapter(t *testing.T) {
	providerSrv, _ := newTestOAuthProvider(t)
	defer providerSrv.Close()

	tokenStore, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatal(err)
	}
	defer tokenStore.Close()

	yamlStr := fmt.Sprintf(`
oauth_providers:
  testprovider:
    auth_url: %s/authorize
    token_url: %s/token
    client_id_env: TEST_OAUTH_CLIENT_ID
    client_secret_env: TEST_OAUTH_CLIENT_SECRET

upstreams:
  - name: test-upstream
    transport: streamable-http
    url: https://test-upstream.example.com
    auth:
      type: oauth
      provider: testprovider
`, providerSrv.URL, providerSrv.URL)

	cfg, err := config.LoadBytes([]byte(yamlStr))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	resolver := NewOAuthResolver(cfg.Upstreams(), tokenStore, nil)
	handler := NewOAuthHandler(cfg.OAuthProviders(), tokenStore, "https://stile.example.com")
	checker := NewConnectionChecker(resolver, handler)

	ctx := context.Background()

	// Not connected → returns provider name.
	ok, prov := checker.IsConnected(ctx, "alice", "test-upstream")
	if ok || prov != "testprovider" {
		t.Errorf("expected not connected, got connected=%v, provider=%q", ok, prov)
	}

	// ConnectURL should be a signed URL.
	connectURL := checker.ConnectURL("alice", "testprovider")
	if !strings.Contains(connectURL, "/oauth/connect/testprovider") {
		t.Errorf("connect URL missing path: %s", connectURL)
	}
	if !strings.Contains(connectURL, "tok=") {
		t.Errorf("connect URL missing tok param: %s", connectURL)
	}

	// Store token → now connected.
	err = tokenStore.StoreToken(ctx, "alice", "testprovider", &OAuthToken{AccessToken: "x"})
	if err != nil {
		t.Fatal(err)
	}
	ok, prov = checker.IsConnected(ctx, "alice", "test-upstream")
	if !ok {
		t.Error("expected connected after storing token")
	}
}
