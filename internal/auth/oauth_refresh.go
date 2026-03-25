package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/anguslmm/stile/internal/config"
)

// TokenRefresher handles refreshing expired OAuth tokens.
type TokenRefresher struct {
	providers map[string]*config.OAuthProviderConfig
	secrets   map[string]oauthSecrets // provider name → resolved secrets
	client    *http.Client
}

type oauthSecrets struct {
	clientID     string
	clientSecret string
}

// NewTokenRefresher creates a refresher that knows how to exchange refresh tokens.
func NewTokenRefresher(providers []config.OAuthProviderConfig, client *http.Client) *TokenRefresher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	pMap := make(map[string]*config.OAuthProviderConfig, len(providers))
	secrets := make(map[string]oauthSecrets, len(providers))
	for i := range providers {
		p := &providers[i]
		pMap[p.Name()] = p
		secrets[p.Name()] = resolveOAuthSecrets(p)
	}
	return &TokenRefresher{providers: pMap, secrets: secrets, client: client}
}

func resolveOAuthSecrets(p *config.OAuthProviderConfig) oauthSecrets {
	var s oauthSecrets
	if envName := p.ClientIDEnv(); envName != "" {
		s.clientID = os.Getenv(envName)
	}
	if envName := p.ClientSecretEnv(); envName != "" {
		s.clientSecret = os.Getenv(envName)
	}
	return s
}

// Refresh attempts to refresh the token using the provider's token endpoint.
// Returns the new token on success. The caller is responsible for storing it.
func (r *TokenRefresher) Refresh(ctx context.Context, providerName string, refreshToken string) (*OAuthToken, error) {
	provider, ok := r.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("oauth: unknown provider %q", providerName)
	}
	sec := r.secrets[providerName]
	if sec.clientID == "" || sec.clientSecret == "" {
		return nil, fmt.Errorf("oauth: missing credentials for provider %q", providerName)
	}
	if refreshToken == "" {
		return nil, fmt.Errorf("oauth: no refresh token available for provider %q", providerName)
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {sec.clientID},
		"client_secret": {sec.clientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.TokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth: refresh failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("oauth: parse refresh response: %w", err)
	}

	token := &OAuthToken{
		AccessToken:  tokenResp.AccessToken,
		TokenType:    tokenResp.TokenType,
		RefreshToken: tokenResp.RefreshToken,
		Scopes:       tokenResp.Scope,
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	// Keep the old refresh token if the provider didn't issue a new one.
	if token.RefreshToken == "" {
		token.RefreshToken = refreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return token, nil
}

// tokenResponse is the standard OAuth 2.0 token endpoint response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}
