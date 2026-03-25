package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anguslmm/stile/internal/config"
)

// OAuthHandler handles the OAuth authorization code flow endpoints.
type OAuthHandler struct {
	providers map[string]*providerInfo
	store     TokenStore
	baseURL   string // e.g. "https://stile.example.com"

	mu       sync.Mutex
	pending  map[string]*pendingAuth // state → pending auth
	client   *http.Client
}

type providerInfo struct {
	config       *config.OAuthProviderConfig
	clientID     string
	clientSecret string
}

type pendingAuth struct {
	user         string
	provider     string
	codeVerifier string
	createdAt    time.Time
}

// NewOAuthHandler creates an OAuth handler.
// baseURL is the externally-reachable URL of this Stile instance (e.g. "https://stile.example.com").
func NewOAuthHandler(providers []config.OAuthProviderConfig, store TokenStore, baseURL string) *OAuthHandler {
	if baseURL == "" {
		baseURL = os.Getenv("STILE_BASE_URL")
	}
	pMap := make(map[string]*providerInfo, len(providers))
	for i := range providers {
		p := &providers[i]
		pMap[p.Name()] = &providerInfo{
			config:       p,
			clientID:     os.Getenv(p.ClientIDEnv()),
			clientSecret: os.Getenv(p.ClientSecretEnv()),
		}
	}
	return &OAuthHandler{
		providers: pMap,
		store:     store,
		baseURL:   strings.TrimRight(baseURL, "/"),
		pending:   make(map[string]*pendingAuth),
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Register registers the OAuth endpoints on the given mux.
func (h *OAuthHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/connect/{provider}", h.handleConnect)
	mux.HandleFunc("GET /oauth/callback", h.handleCallback)
}

// handleConnect starts the OAuth authorization code flow.
// The user must be authenticated (caller in context).
func (h *OAuthHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")

	caller := CallerFromContext(r.Context())
	if caller == nil {
		http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
		return
	}

	pi, ok := h.providers[providerName]
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"unknown provider %q"}`, providerName), http.StatusNotFound)
		return
	}

	if pi.clientID == "" {
		http.Error(w, fmt.Sprintf(`{"error":"OAuth not configured for provider %q"}`, providerName), http.StatusInternalServerError)
		return
	}

	// Generate PKCE code verifier and challenge.
	verifier, err := generateCodeVerifier()
	if err != nil {
		http.Error(w, `{"error":"internal error generating PKCE verifier"}`, http.StatusInternalServerError)
		return
	}
	challenge := computeCodeChallenge(verifier)

	// Generate state parameter (CSRF protection).
	state, err := generateState()
	if err != nil {
		http.Error(w, `{"error":"internal error generating state"}`, http.StatusInternalServerError)
		return
	}

	// Store pending auth.
	h.mu.Lock()
	h.pending[state] = &pendingAuth{
		user:         caller.Name,
		provider:     providerName,
		codeVerifier: verifier,
		createdAt:    time.Now(),
	}
	h.mu.Unlock()

	// Clean up stale pending entries.
	go h.cleanupStale()

	// Build authorization URL.
	redirectURI := h.baseURL + "/oauth/callback"
	params := url.Values{
		"client_id":             {pi.clientID},
		"response_type":        {"code"},
		"redirect_uri":         {redirectURI},
		"state":                {state},
		"code_challenge":       {challenge},
		"code_challenge_method": {"S256"},
	}
	if scopes := pi.config.Scopes(); len(scopes) > 0 {
		params.Set("scope", strings.Join(scopes, " "))
	}

	authURL := pi.config.AuthURL() + "?" + params.Encode()
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleCallback handles the OAuth provider's redirect after authorization.
func (h *OAuthHandler) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Check for error from provider.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		slog.Warn("oauth callback error from provider", "error", errParam, "description", desc)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "<html><body><h2>Authorization Failed</h2><p>%s: %s</p></body></html>", errParam, desc)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if state == "" || code == "" {
		http.Error(w, `{"error":"missing state or code parameter"}`, http.StatusBadRequest)
		return
	}

	// Look up and consume the pending auth.
	h.mu.Lock()
	pa, ok := h.pending[state]
	if ok {
		delete(h.pending, state)
	}
	h.mu.Unlock()

	if !ok {
		http.Error(w, `{"error":"invalid or expired state parameter"}`, http.StatusBadRequest)
		return
	}

	// Check expiry (10 minute max).
	if time.Since(pa.createdAt) > 10*time.Minute {
		http.Error(w, `{"error":"authorization flow expired, please try again"}`, http.StatusBadRequest)
		return
	}

	pi, ok := h.providers[pa.provider]
	if !ok {
		http.Error(w, `{"error":"provider configuration missing"}`, http.StatusInternalServerError)
		return
	}

	// Exchange authorization code for tokens.
	redirectURI := h.baseURL + "/oauth/callback"
	token, err := h.exchangeCode(r.Context(), pi, code, redirectURI, pa.codeVerifier)
	if err != nil {
		slog.Error("oauth token exchange failed", "provider", pa.provider, "error", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "<html><body><h2>Token Exchange Failed</h2><p>%s</p></body></html>", err.Error())
		return
	}

	// Store the token.
	if err := h.store.StoreToken(r.Context(), pa.user, pa.provider, token); err != nil {
		slog.Error("failed to store oauth token", "provider", pa.provider, "user", pa.user, "error", err)
		http.Error(w, `{"error":"failed to store token"}`, http.StatusInternalServerError)
		return
	}

	slog.Info("oauth connection established", "provider", pa.provider, "user", pa.user)

	// Success page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><body>
<h2>Connected!</h2>
<p>Successfully connected your <strong>%s</strong> account. You can close this window.</p>
</body></html>`, pa.provider)
}

func (h *OAuthHandler) exchangeCode(ctx context.Context, pi *providerInfo, code, redirectURI, codeVerifier string) (*OAuthToken, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {pi.clientID},
		"client_secret": {pi.clientSecret},
		"code_verifier": {codeVerifier},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pi.config.TokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}

	token := &OAuthToken{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		Scopes:       tokenResp.Scope,
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	if tokenResp.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return token, nil
}

// cleanupStale removes pending auth entries older than 15 minutes.
func (h *OAuthHandler) cleanupStale() {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := time.Now().Add(-15 * time.Minute)
	for k, v := range h.pending {
		if v.createdAt.Before(cutoff) {
			delete(h.pending, k)
		}
	}
}

// generateState creates a cryptographically random state parameter.
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeVerifier creates a PKCE code verifier (43-128 chars, RFC 7636).
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32) // 32 bytes → 43 base64url chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// computeCodeChallenge computes the S256 PKCE code challenge from a verifier.
func computeCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
