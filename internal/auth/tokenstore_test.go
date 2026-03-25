package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSQLiteTokenStore_CRUD(t *testing.T) {
	store, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initially, no providers connected.
	providers, err := store.ListProviders(ctx, "alice")
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 0 {
		t.Fatalf("expected no providers, got %v", providers)
	}

	// GetToken on missing token returns ErrNotFound.
	_, err = store.GetToken(ctx, "alice", "github")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Store a token.
	expiry := time.Now().Add(1 * time.Hour).Truncate(time.Second)
	token := &OAuthToken{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		TokenType:    "Bearer",
		Expiry:       expiry,
		Scopes:       "repo read:org",
	}
	if err := store.StoreToken(ctx, "alice", "github", token); err != nil {
		t.Fatalf("StoreToken: %v", err)
	}

	// Get the token back.
	got, err := store.GetToken(ctx, "alice", "github")
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.AccessToken != "access-123" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "access-123")
	}
	if got.RefreshToken != "refresh-456" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "refresh-456")
	}
	if got.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want %q", got.TokenType, "Bearer")
	}
	if got.Scopes != "repo read:org" {
		t.Errorf("Scopes = %q, want %q", got.Scopes, "repo read:org")
	}
	// Compare with 1-second tolerance for time rounding.
	if got.Expiry.Sub(expiry).Abs() > time.Second {
		t.Errorf("Expiry = %v, want %v", got.Expiry, expiry)
	}

	// List providers.
	providers, err = store.ListProviders(ctx, "alice")
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 1 || providers[0] != "github" {
		t.Fatalf("expected [github], got %v", providers)
	}

	// Update the token (upsert).
	token.AccessToken = "access-789"
	if err := store.StoreToken(ctx, "alice", "github", token); err != nil {
		t.Fatalf("StoreToken (update): %v", err)
	}
	got, err = store.GetToken(ctx, "alice", "github")
	if err != nil {
		t.Fatalf("GetToken after update: %v", err)
	}
	if got.AccessToken != "access-789" {
		t.Errorf("AccessToken after update = %q, want %q", got.AccessToken, "access-789")
	}

	// Delete the token.
	if err := store.DeleteToken(ctx, "alice", "github"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	_, err = store.GetToken(ctx, "alice", "github")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// Double delete returns ErrNotFound.
	err = store.DeleteToken(ctx, "alice", "github")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on double delete, got %v", err)
	}
}

func TestSQLiteTokenStore_MultipleProviders(t *testing.T) {
	store, err := NewSQLiteTokenStore(t.TempDir() + "/tokens.db")
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Store tokens for two providers.
	if err := store.StoreToken(ctx, "alice", "github", &OAuthToken{AccessToken: "gh-token"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreToken(ctx, "alice", "notion", &OAuthToken{AccessToken: "notion-token"}); err != nil {
		t.Fatal(err)
	}

	providers, err := store.ListProviders(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %v", providers)
	}
	// Sorted alphabetically.
	if providers[0] != "github" || providers[1] != "notion" {
		t.Fatalf("expected [github, notion], got %v", providers)
	}

	// Different user sees no providers.
	providers, err = store.ListProviders(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 0 {
		t.Fatalf("expected no providers for bob, got %v", providers)
	}
}

func TestOAuthToken_Expired(t *testing.T) {
	// Token with no expiry is never expired.
	token := &OAuthToken{AccessToken: "test"}
	if token.Expired() {
		t.Error("token with no expiry should not be expired")
	}

	// Token expiring in the future (well beyond the 30s buffer).
	token.Expiry = time.Now().Add(5 * time.Minute)
	if token.Expired() {
		t.Error("token expiring in 5 minutes should not be expired")
	}

	// Token that expired in the past.
	token.Expiry = time.Now().Add(-1 * time.Minute)
	if !token.Expired() {
		t.Error("token that expired 1 minute ago should be expired")
	}

	// Token expiring within 30-second buffer.
	token.Expiry = time.Now().Add(10 * time.Second)
	if !token.Expired() {
		t.Error("token expiring in 10 seconds should be considered expired (30s buffer)")
	}
}
