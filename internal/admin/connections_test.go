package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/testutil"
)

func newTestTokenStore(t *testing.T) *auth.SQLiteTokenStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tokens.db")
	ts, err := auth.NewSQLiteTokenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ts.Close() })
	return ts
}

func newTestServerWithTokenStore(t *testing.T, ts auth.TokenStore, providers []string) *httptest.Server {
	t.Helper()
	store := newTestStore(t)
	rt := newTestRouter(t)
	opts := []Option{
		WithTokenStore(ts),
		WithOAuthProviders(providers),
	}
	h := NewHandler(store, rt, opts...)
	mux := http.NewServeMux()
	h.Register(mux)
	return testutil.NewServer(mux)
}

func TestListConnections_NoOAuth(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	h := NewHandler(store, rt)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := testutil.NewServer(mux)
	defer srv.Close()

	resp := doRequest(t, "GET", srv.URL+"/admin/connections", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result map[string]any
	readJSON(t, resp, &result)
	if result["message"] != "OAuth not configured" {
		t.Fatalf("expected 'OAuth not configured', got %v", result["message"])
	}
}

func TestListConnections_WithProviders(t *testing.T) {
	ts := newTestTokenStore(t)
	providers := []string{"github", "google"}
	srv := newTestServerWithTokenStore(t, ts, providers)
	defer srv.Close()

	// No caller param — should list providers with no connection info.
	resp := doRequest(t, "GET", srv.URL+"/admin/connections", nil)
	var result struct {
		Connections []struct {
			Provider  string `json:"provider"`
			Connected bool   `json:"connected"`
		} `json:"connections"`
	}
	readJSON(t, resp, &result)

	if len(result.Connections) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(result.Connections))
	}
	if result.Connections[0].Provider != "github" {
		t.Fatalf("expected github, got %s", result.Connections[0].Provider)
	}
	if result.Connections[0].Connected {
		t.Fatal("expected not connected")
	}
}

func TestListConnections_WithConnectedUser(t *testing.T) {
	ts := newTestTokenStore(t)
	providers := []string{"github", "google"}
	srv := newTestServerWithTokenStore(t, ts, providers)
	defer srv.Close()

	// Store a token for alice on github.
	err := ts.StoreToken(t.Context(), "alice@example.com", "github", &auth.OAuthToken{
		AccessToken: "tok-alice",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
		Scopes:      "repo user",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := doRequest(t, "GET", srv.URL+"/admin/connections?caller=alice@example.com", nil)
	var result struct {
		Connections []struct {
			Provider  string     `json:"provider"`
			Connected bool       `json:"connected"`
			Expired   bool       `json:"expired"`
			Scopes    string     `json:"scopes"`
			Expiry    *time.Time `json:"expiry"`
		} `json:"connections"`
	}
	readJSON(t, resp, &result)

	if len(result.Connections) != 2 {
		t.Fatalf("expected 2, got %d", len(result.Connections))
	}
	// github should be connected
	gh := result.Connections[0]
	if !gh.Connected {
		t.Fatal("github should be connected")
	}
	if gh.Scopes != "repo user" {
		t.Fatalf("expected 'repo user', got %q", gh.Scopes)
	}
	if gh.Expired {
		t.Fatal("should not be expired")
	}
	// google should not be connected
	if result.Connections[1].Connected {
		t.Fatal("google should not be connected")
	}
}

func TestPutConnection(t *testing.T) {
	ts := newTestTokenStore(t)
	providers := []string{"mock"}
	srv := newTestServerWithTokenStore(t, ts, providers)
	defer srv.Close()

	body := map[string]string{
		"caller":       "alice@example.com",
		"access_token": "test-token-for-alice",
	}
	resp := doRequest(t, "PUT", srv.URL+"/admin/connections/mock", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result map[string]string
	readJSON(t, resp, &result)
	if result["status"] != "ok" {
		t.Fatalf("expected ok, got %s", result["status"])
	}

	// Verify the token was stored.
	token, err := ts.GetToken(t.Context(), "alice@example.com", "mock")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "test-token-for-alice" {
		t.Fatalf("expected test-token-for-alice, got %s", token.AccessToken)
	}
}

func TestPutConnection_MissingCaller(t *testing.T) {
	ts := newTestTokenStore(t)
	srv := newTestServerWithTokenStore(t, ts, []string{"mock"})
	defer srv.Close()

	body := map[string]string{"access_token": "tok"}
	resp := doRequest(t, "PUT", srv.URL+"/admin/connections/mock", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPutConnection_MissingToken(t *testing.T) {
	ts := newTestTokenStore(t)
	srv := newTestServerWithTokenStore(t, ts, []string{"mock"})
	defer srv.Close()

	body := map[string]string{"caller": "alice@example.com"}
	resp := doRequest(t, "PUT", srv.URL+"/admin/connections/mock", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDeleteConnection(t *testing.T) {
	ts := newTestTokenStore(t)
	providers := []string{"mock"}
	srv := newTestServerWithTokenStore(t, ts, providers)
	defer srv.Close()

	// Store a token first.
	err := ts.StoreToken(t.Context(), "alice@example.com", "mock", &auth.OAuthToken{
		AccessToken: "tok",
		TokenType:   "Bearer",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Delete it.
	resp := doRequest(t, "DELETE", srv.URL+"/admin/connections/mock?caller=alice@example.com", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify it's gone.
	_, err = ts.GetToken(t.Context(), "alice@example.com", "mock")
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestDeleteConnection_NotFound(t *testing.T) {
	ts := newTestTokenStore(t)
	srv := newTestServerWithTokenStore(t, ts, []string{"mock"})
	defer srv.Close()

	resp := doRequest(t, "DELETE", srv.URL+"/admin/connections/mock?caller=alice@example.com", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDeleteConnection_MissingCaller(t *testing.T) {
	ts := newTestTokenStore(t)
	srv := newTestServerWithTokenStore(t, ts, []string{"mock"})
	defer srv.Close()

	resp := doRequest(t, "DELETE", srv.URL+"/admin/connections/mock", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
