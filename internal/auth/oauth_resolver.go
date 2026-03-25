package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anguslmm/stile/internal/config"
)

// OAuthResolver resolves per-user OAuth tokens for upstream requests.
// It implements proxy.UpstreamAuthResolver.
type OAuthResolver struct {
	store     TokenStore
	refresher *TokenRefresher
	// upstreamProvider maps upstream name → OAuth provider name.
	upstreamProvider map[string]string
}

// NewOAuthResolver creates a resolver from config, token store, and refresher.
func NewOAuthResolver(upstreams []config.UpstreamConfig, store TokenStore, refresher *TokenRefresher) *OAuthResolver {
	m := make(map[string]string)
	for _, u := range upstreams {
		h, ok := u.(*config.HTTPUpstreamConfig)
		if !ok {
			continue
		}
		if a := h.Auth(); a != nil && a.Type() == "oauth" {
			m[h.Name()] = a.Provider()
		}
	}
	return &OAuthResolver{store: store, refresher: refresher, upstreamProvider: m}
}

// ResolveToken returns the bearer token for the given user and upstream.
// Returns ("", nil) if the upstream doesn't use OAuth.
// Returns an error if the user hasn't connected the required provider.
func (r *OAuthResolver) ResolveToken(ctx context.Context, callerName, upstreamName string) (string, error) {
	providerName, ok := r.upstreamProvider[upstreamName]
	if !ok {
		return "", nil // not an OAuth upstream
	}

	token, err := r.store.GetToken(ctx, callerName, providerName)
	if err != nil {
		return "", fmt.Errorf("user has not connected %s — visit /oauth/connect/%s to authorize", providerName, providerName)
	}

	// Refresh if expired.
	if token.Expired() && token.RefreshToken != "" {
		newToken, err := r.refresher.Refresh(ctx, providerName, token.RefreshToken)
		if err != nil {
			slog.WarnContext(ctx, "oauth token refresh failed",
				"provider", providerName,
				"user", callerName,
				"error", err,
			)
			// If refresh fails but we still have an access token, try it anyway —
			// the token might have been refreshed by another instance.
			if token.AccessToken != "" {
				return token.AccessToken, nil
			}
			return "", fmt.Errorf("oauth token for %s has expired and refresh failed: %v", providerName, err)
		}
		// Store the refreshed token.
		if err := r.store.StoreToken(ctx, callerName, providerName, newToken); err != nil {
			slog.WarnContext(ctx, "failed to store refreshed token",
				"provider", providerName,
				"user", callerName,
				"error", err,
			)
		}
		return newToken.AccessToken, nil
	}

	return token.AccessToken, nil
}
