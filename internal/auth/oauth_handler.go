package auth

import (
	"context"
	"crypto/hmac"
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
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/anguslmm/stile/internal/config"
)

// connectTokenTTL is the lifetime of signed connect URL tokens.
const connectTokenTTL = 5 * time.Minute

// stateExpiry is how long an OAuth authorization flow is valid.
const stateExpiry = 10 * time.Minute

// OAuthHandler handles the OAuth authorization code flow endpoints.
type OAuthHandler struct {
	providers  map[string]*providerInfo
	store      TokenStore
	baseURL    string // e.g. "https://stile.example.com"
	signingKey []byte // HMAC-SHA256 key for JWTs and signed connect URLs

	client *http.Client
}

type providerInfo struct {
	config       *config.OAuthProviderConfig
	clientID     string
	clientSecret string
}

// stateClaims are the JWT claims encoded into the OAuth state parameter.
// This makes the state self-contained so any instance can handle the callback.
type stateClaims struct {
	jwt.RegisteredClaims
	Provider     string `json:"provider"`
	CodeVerifier string `json:"verifier"`
}

// OAuthHandlerOption configures an OAuthHandler.
type OAuthHandlerOption func(*OAuthHandler)

// WithSigningKey sets the HMAC-SHA256 signing key for JWTs and connect URL tokens.
// All instances in a multi-instance deployment must share the same key.
// If not set, a random key is generated (only works for single-instance).
func WithSigningKey(key []byte) OAuthHandlerOption {
	return func(h *OAuthHandler) {
		if len(key) > 0 {
			h.signingKey = key
		}
	}
}

// NewOAuthHandler creates an OAuth handler.
// baseURL is the externally-reachable URL of this Stile instance (e.g. "https://stile.example.com").
func NewOAuthHandler(providers []config.OAuthProviderConfig, store TokenStore, baseURL string, opts ...OAuthHandlerOption) *OAuthHandler {
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
	h := &OAuthHandler{
		providers: pMap,
		store:     store,
		baseURL:   strings.TrimRight(baseURL, "/"),
		client:    &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(h)
	}
	// Default: random signing key (single-instance only).
	if len(h.signingKey) == 0 {
		h.signingKey = make([]byte, 32)
		if _, err := rand.Read(h.signingKey); err != nil {
			slog.Error("failed to generate signing key", "error", err)
		}
	}
	return h
}

// Register registers the OAuth endpoints on the given mux.
func (h *OAuthHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/connect/{provider}", h.handleConnect)
	mux.HandleFunc("GET /oauth/callback", h.handleCallback)
}

// handleConnect starts the OAuth authorization code flow.
// The user must be authenticated via either:
//   - Caller in context (from Authenticator middleware, for API clients)
//   - A signed "tok" query parameter (for browser-based flows)
func (h *OAuthHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")

	// Try caller from context first (API key / OIDC via middleware).
	callerName := ""
	if caller := CallerFromContext(r.Context()); caller != nil {
		callerName = caller.Name
	}

	// Fall back to signed connect token in query string.
	if callerName == "" {
		if tok := r.URL.Query().Get("tok"); tok != "" {
			name, err := h.verifyConnectToken(tok)
			if err != nil {
				slog.Debug("invalid connect token", "error", err)
				http.Error(w, `{"error":"invalid or expired connect token"}`, http.StatusUnauthorized)
				return
			}
			callerName = name
		}
	}

	if callerName == "" {
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

	// Build a JWT state that encodes the pending auth. This is self-contained
	// so any Stile instance (with the same signing key) can handle the callback.
	state, err := h.signState(callerName, providerName, verifier)
	if err != nil {
		slog.Error("failed to sign OAuth state", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

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
//
// Security model (34.1.3 audit):
//
// This endpoint is intentionally unauthenticated. It is protected by:
//
//  1. State parameter: A signed JWT containing the caller, provider, and PKCE
//     verifier. Only Stile instances with the shared signing key can create or
//     verify valid states. 10-minute expiry via the JWT exp claim.
//
//  2. PKCE (S256): The code_verifier is embedded in the signed state JWT.
//     Only Stile can exchange the authorization code because only Stile knows
//     the verifier. This prevents authorization code injection.
//
//  3. Caller binding: The JWT sub claim binds the state to a specific caller.
//     Tokens are stored under that caller, not whoever hits the callback.
//
//  4. Authorization code single-use: The OAuth provider enforces that each
//     authorization code can only be exchanged once.
//
// Cross-user attack scenario: User A starts a flow, user B intercepts the
// callback URL (state+code). B hits the callback, but the token is stored under
// A's name (from the JWT sub claim), not B's. B gains nothing.
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

	stateStr := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if stateStr == "" || code == "" {
		http.Error(w, `{"error":"missing state or code parameter"}`, http.StatusBadRequest)
		return
	}

	// Verify and parse the JWT state.
	claims, err := h.verifyState(stateStr)
	if err != nil {
		slog.Debug("invalid OAuth state", "error", err)
		http.Error(w, `{"error":"invalid or expired state parameter"}`, http.StatusBadRequest)
		return
	}

	pi, ok := h.providers[claims.Provider]
	if !ok {
		http.Error(w, `{"error":"provider configuration missing"}`, http.StatusInternalServerError)
		return
	}

	callerName := claims.Subject

	// Exchange authorization code for tokens.
	redirectURI := h.baseURL + "/oauth/callback"
	token, err := h.exchangeCode(r.Context(), pi, code, redirectURI, claims.CodeVerifier)
	if err != nil {
		slog.Error("oauth token exchange failed", "provider", claims.Provider, "error", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "<html><body><h2>Token Exchange Failed</h2><p>%s</p></body></html>", err.Error())
		return
	}

	// Store the token.
	if err := h.store.StoreToken(r.Context(), callerName, claims.Provider, token); err != nil {
		slog.Error("failed to store oauth token", "provider", claims.Provider, "user", callerName, "error", err)
		http.Error(w, `{"error":"failed to store token"}`, http.StatusInternalServerError)
		return
	}

	slog.Info("oauth connection established", "provider", claims.Provider, "user", callerName)

	// Success page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><body>
<h2>Connected!</h2>
<p>Successfully connected your <strong>%s</strong> account. You can close this window.</p>
</body></html>`, claims.Provider)
}

// signState creates a signed JWT encoding the pending OAuth flow data.
func (h *OAuthHandler) signState(callerName, provider, codeVerifier string) (string, error) {
	claims := stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   callerName,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(stateExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Provider:     provider,
		CodeVerifier: codeVerifier,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.signingKey)
}

// verifyState parses and validates a JWT state parameter from the callback.
func (h *OAuthHandler) verifyState(tokenStr string) (*stateClaims, error) {
	var claims stateClaims
	_, err := jwt.ParseWithClaims(tokenStr, &claims, func(_ *jwt.Token) (any, error) {
		return h.signingKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	if claims.Subject == "" || claims.Provider == "" || claims.CodeVerifier == "" {
		return nil, fmt.Errorf("incomplete state claims")
	}
	return &claims, nil
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

// GenerateConnectURL creates a signed URL that a browser can use to start the
// OAuth connection flow for the given caller and provider. The URL is valid for 5 minutes.
func (h *OAuthHandler) GenerateConnectURL(callerName, provider string) string {
	tok := h.signConnectToken(callerName)
	return fmt.Sprintf("%s/oauth/connect/%s?tok=%s", h.baseURL, url.PathEscape(provider), url.QueryEscape(tok))
}

// signConnectToken creates an HMAC-signed token encoding the caller name and expiry.
// Format: base64url(caller|expiry_unix|hmac_base64url).
func (h *OAuthHandler) signConnectToken(callerName string) string {
	expiry := time.Now().Add(connectTokenTTL).Unix()
	payload := callerName + "|" + strconv.FormatInt(expiry, 10)
	mac := hmac.New(sha256.New, h.signingKey)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	raw := payload + "|" + sig
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// verifyConnectToken validates a signed connect token and returns the caller name.
func (h *OAuthHandler) verifyConnectToken(tok string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return "", fmt.Errorf("decode token: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed token")
	}
	callerName, expiryStr, sig := parts[0], parts[1], parts[2]

	// Verify HMAC.
	payload := callerName + "|" + expiryStr
	mac := hmac.New(sha256.New, h.signingKey)
	mac.Write([]byte(payload))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", fmt.Errorf("invalid signature")
	}

	// Verify expiry.
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid expiry: %w", err)
	}
	if time.Now().Unix() > expiry {
		return "", fmt.Errorf("token expired")
	}

	return callerName, nil
}

// ProviderNames returns the names of all configured OAuth providers.
func (h *OAuthHandler) ProviderNames() []string {
	names := make([]string, 0, len(h.providers))
	for name := range h.providers {
		names = append(names, name)
	}
	return names
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
