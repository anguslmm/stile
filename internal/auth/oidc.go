package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/anguslmm/stile/internal/config"
)

// OIDCValidator validates inbound OIDC tokens using either JWT or userinfo mode.
type OIDCValidator struct {
	cfg      *config.OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier

	// Userinfo cache (userinfo mode only).
	userinfoMu    sync.RWMutex
	userinfoCache map[string]*userinfoEntry
	cacheTTL      time.Duration

	// userinfoURL is the provider's userinfo endpoint (userinfo mode only).
	userinfoURL string

	httpClient *http.Client
}

type userinfoEntry struct {
	claims    map[string]interface{}
	expiresAt time.Time
}

// OIDCOption configures an OIDCValidator.
type OIDCOption func(*OIDCValidator)

// WithOIDCHTTPClient sets the HTTP client used for OIDC discovery and userinfo calls.
func WithOIDCHTTPClient(client *http.Client) OIDCOption {
	return func(v *OIDCValidator) {
		v.httpClient = client
	}
}

// NewOIDCValidator creates an OIDC validator by discovering the provider's configuration.
func NewOIDCValidator(ctx context.Context, cfg *config.OIDCConfig, opts ...OIDCOption) (*OIDCValidator, error) {
	v := &OIDCValidator{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		cacheTTL:   5 * time.Minute,
	}
	for _, opt := range opts {
		opt(v)
	}

	provCtx := oidc.ClientContext(ctx, v.httpClient)
	provider, err := oidc.NewProvider(provCtx, cfg.Issuer())
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery failed for %q: %w", cfg.Issuer(), err)
	}
	v.provider = provider

	switch cfg.Validation() {
	case "jwt":
		v.verifier = provider.Verifier(&oidc.Config{
			ClientID: cfg.Audience(),
		})
	case "userinfo":
		var meta struct {
			UserInfoURL string `json:"userinfo_endpoint"`
		}
		if err := provider.Claims(&meta); err != nil {
			return nil, fmt.Errorf("auth: failed to read provider metadata: %w", err)
		}
		if meta.UserInfoURL == "" {
			return nil, fmt.Errorf("auth: provider %q does not expose a userinfo endpoint", cfg.Issuer())
		}
		v.userinfoURL = meta.UserInfoURL
		v.userinfoCache = make(map[string]*userinfoEntry)
	}

	return v, nil
}

// Validate validates the token and returns the caller identity.
func (v *OIDCValidator) Validate(ctx context.Context, token string) (string, error) {
	var claims map[string]interface{}
	var err error

	switch v.cfg.Validation() {
	case "jwt":
		claims, err = v.validateJWT(ctx, token)
	case "userinfo":
		claims, err = v.validateUserinfo(ctx, token)
	default:
		return "", fmt.Errorf("auth: unsupported OIDC validation mode %q", v.cfg.Validation())
	}
	if err != nil {
		return "", err
	}

	// Extract caller identity from the configured claim.
	identity, ok := claims[v.cfg.CallerClaim()].(string)
	if !ok || identity == "" {
		return "", fmt.Errorf("auth: OIDC token missing claim %q", v.cfg.CallerClaim())
	}

	// Domain filtering.
	if domains := v.cfg.AllowedDomains(); len(domains) > 0 {
		email, _ := claims["email"].(string)
		if email == "" {
			return "", fmt.Errorf("auth: OIDC token missing email for domain filter")
		}
		if !v.domainAllowed(email) {
			return "", fmt.Errorf("auth: email domain not in allowed list")
		}
	}

	return identity, nil
}

func (v *OIDCValidator) validateJWT(ctx context.Context, token string) (map[string]interface{}, error) {
	verifyCtx := oidc.ClientContext(ctx, v.httpClient)
	idToken, err := v.verifier.Verify(verifyCtx, token)
	if err != nil {
		return nil, fmt.Errorf("auth: JWT verification failed: %w", err)
	}

	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("auth: JWT claims extraction failed: %w", err)
	}
	return claims, nil
}

func (v *OIDCValidator) validateUserinfo(ctx context.Context, token string) (map[string]interface{}, error) {
	// Check cache.
	tokenHash := sha256.Sum256([]byte(token))
	cacheKey := fmt.Sprintf("%x", tokenHash)

	v.userinfoMu.RLock()
	if entry, ok := v.userinfoCache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		v.userinfoMu.RUnlock()
		return entry.claims, nil
	}
	v.userinfoMu.RUnlock()

	// Call userinfo endpoint.
	req, err := http.NewRequestWithContext(ctx, "GET", v.userinfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: userinfo returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("auth: read userinfo response: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, fmt.Errorf("auth: parse userinfo response: %w", err)
	}

	// Cache result.
	v.userinfoMu.Lock()
	v.userinfoCache[cacheKey] = &userinfoEntry{
		claims:    claims,
		expiresAt: time.Now().Add(v.cacheTTL),
	}
	v.userinfoMu.Unlock()

	return claims, nil
}

func (v *OIDCValidator) domainAllowed(email string) bool {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return false
	}
	domain := parts[1]
	for _, allowed := range v.cfg.AllowedDomains() {
		if strings.EqualFold(domain, allowed) {
			return true
		}
	}
	return false
}

// isJWT reports whether the token looks like a JWT (three dot-separated segments).
func isJWT(token string) bool {
	return strings.Count(token, ".") == 2
}
