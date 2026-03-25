// Package auth provides inbound authentication, per-caller tool access
// control, and per-caller outbound credential injection.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/gobwas/glob"

	"github.com/anguslmm/stile/internal/config"
)

// AuthMethodKey is a context key for the authentication method used.
type authMethodKey struct{}

// AuthMethodFromContext returns the authentication method ("apikey" or "oidc") from the context.
func AuthMethodFromContext(ctx context.Context) string {
	s, _ := ctx.Value(authMethodKey{}).(string)
	return s
}

// ContextWithAuthMethod attaches the auth method to the context.
func ContextWithAuthMethod(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, authMethodKey{}, method)
}

// Caller represents an authenticated caller with their access permissions.
type Caller struct {
	Name         string
	Roles        []string    // all roles assigned to this caller, in config order
	AllowedTools []glob.Glob // union of all roles' patterns
}

// CanAccessTool reports whether the caller is allowed to use the named tool.
func (c *Caller) CanAccessTool(toolName string) bool {
	for _, g := range c.AllowedTools {
		if g.Match(toolName) {
			return true
		}
	}
	return false
}

// KeyLookupResult is returned by LookupByKey with both caller identity and key metadata.
type KeyLookupResult struct {
	Caller   *Caller
	KeyLabel string
}

type contextKey struct{}
type keyLabelKey struct{}
type upstreamTokenKey struct{}

// CallerFromContext retrieves the Caller from the request context.
// Returns nil if no caller is set (auth disabled).
func CallerFromContext(ctx context.Context) *Caller {
	c, _ := ctx.Value(contextKey{}).(*Caller)
	return c
}

// ContextWithCaller returns a new context with the given Caller attached.
func ContextWithCaller(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// KeyLabelFromContext retrieves the API key label from the request context.
// Returns "" if no key label is set.
func KeyLabelFromContext(ctx context.Context) string {
	s, _ := ctx.Value(keyLabelKey{}).(string)
	return s
}

// ContextWithKeyLabel returns a new context with the given key label attached.
func ContextWithKeyLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, keyLabelKey{}, label)
}

// UpstreamTokenFromContext retrieves the per-request OAuth token for the upstream.
// Returns "" if no token is set (static auth or no auth).
func UpstreamTokenFromContext(ctx context.Context) string {
	s, _ := ctx.Value(upstreamTokenKey{}).(string)
	return s
}

// ContextWithUpstreamToken returns a new context with the given upstream token attached.
func ContextWithUpstreamToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, upstreamTokenKey{}, token)
}

// CallerStore looks up callers by API key hash.
type CallerStore interface {
	LookupByKey(hashedKey [32]byte) (*KeyLookupResult, error)
	RolesForCaller(name string) ([]string, error)
}

// Authenticator handles inbound authentication and credential injection.
type Authenticator struct {
	store        CallerStore
	credentials  map[string]map[string]string // role name → upstream name → token value
	allowedTools map[string][]glob.Glob       // role name → compiled patterns
	roleOrder    []string                     // role names in config order
	oidc         *OIDCValidator
	oidcCfg      *config.OIDCConfig
}

// AuthenticatorOption configures the Authenticator.
type AuthenticatorOption func(*Authenticator)

// WithOIDCValidator attaches an OIDC validator to the authenticator.
func WithOIDCValidator(v *OIDCValidator, cfg *config.OIDCConfig) AuthenticatorOption {
	return func(a *Authenticator) {
		a.oidc = v
		a.oidcCfg = cfg
	}
}

// NewAuthenticator creates an Authenticator, resolving role config into
// actual token values by reading environment variables and compiling glob
// patterns for tool access.
func NewAuthenticator(store CallerStore, roles []config.RoleConfig, opts ...AuthenticatorOption) *Authenticator {
	creds := make(map[string]map[string]string, len(roles))
	globs := make(map[string][]glob.Glob, len(roles))
	order := make([]string, 0, len(roles))

	for _, role := range roles {
		order = append(order, role.Name())

		tokens := make(map[string]string)
		for upstream, envVar := range role.Credentials() {
			val := os.Getenv(envVar)
			if val == "" {
				slog.Warn("env var not set", "env_var", envVar, "role", role.Name(), "upstream", upstream)
				continue
			}
			tokens[upstream] = val
		}
		creds[role.Name()] = tokens

		var compiled []glob.Glob
		for _, pattern := range role.AllowedTools() {
			g, err := glob.Compile(pattern)
			if err != nil {
				slog.Error("invalid glob pattern, skipping", "pattern", pattern, "role", role.Name(), "error", err)
				continue
			}
			compiled = append(compiled, g)
		}
		globs[role.Name()] = compiled
	}

	a := &Authenticator{
		store:        store,
		credentials:  creds,
		allowedTools: globs,
		roleOrder:    order,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Authenticate extracts and validates bearer token from the request.
// Returns the caller, the label of the key used ("oidc" for OIDC auth), and any error.
func (a *Authenticator) Authenticate(r *http.Request) (*Caller, string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, "", fmt.Errorf("missing Authorization header")
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, "", fmt.Errorf("Authorization header must use Bearer scheme")
	}

	token := strings.TrimPrefix(header, "Bearer ")

	// Try OIDC if configured.
	if a.oidc != nil {
		shouldTry := false
		switch a.oidcCfg.Validation() {
		case "jwt":
			shouldTry = isJWT(token)
		case "userinfo":
			// Skip OIDC for tokens that look like Stile API keys.
			shouldTry = !strings.HasPrefix(token, "sk-")
		}
		if shouldTry {
			caller, err := a.authenticateOIDC(r.Context(), token)
			if err == nil {
				return caller, "oidc", nil
			}
			slog.Debug("OIDC validation failed, trying API key", "error", err)
		}
	}

	// API key authentication.
	hash := sha256.Sum256([]byte(token))
	result, err := a.store.LookupByKey(hash)
	if err != nil {
		return nil, "", fmt.Errorf("unauthorized")
	}

	caller := result.Caller

	// Get all roles assigned to this caller and order by config order.
	roles, err := a.store.RolesForCaller(caller.Name)
	if err != nil {
		return nil, "", fmt.Errorf("lookup roles: %w", err)
	}
	caller.Roles = a.orderByConfig(roles)
	caller.AllowedTools = a.unionAllowedTools(caller.Roles)

	return caller, result.KeyLabel, nil
}

// authenticateOIDC validates an OIDC token and returns a Caller.
func (a *Authenticator) authenticateOIDC(ctx context.Context, token string) (*Caller, error) {
	identity, err := a.oidc.Validate(ctx, token)
	if err != nil {
		return nil, err
	}

	// Auto-provision or verify caller exists.
	// The store may implement Store (which includes EnsureCaller/CallerExists).
	if store, ok := a.store.(Store); ok {
		if a.oidcCfg.AutoProvision() {
			if err := store.EnsureCaller(identity, a.oidcCfg.DefaultRoles()); err != nil {
				slog.Warn("OIDC auto-provision failed", "identity", identity, "error", err)
			}
		} else {
			exists, err := store.CallerExists(identity)
			if err != nil {
				return nil, fmt.Errorf("auth: check caller exists: %w", err)
			}
			if !exists {
				return nil, fmt.Errorf("auth: OIDC caller %q not registered", identity)
			}
		}
	}

	roles, err := a.store.RolesForCaller(identity)
	if err != nil {
		return nil, fmt.Errorf("auth: lookup roles for %q: %w", identity, err)
	}

	caller := &Caller{Name: identity}
	caller.Roles = a.orderByConfig(roles)
	caller.AllowedTools = a.unionAllowedTools(caller.Roles)
	return caller, nil
}

// unionAllowedTools computes the union of glob patterns across the given roles.
func (a *Authenticator) unionAllowedTools(roleNames []string) []glob.Glob {
	var globs []glob.Glob
	for _, name := range roleNames {
		if g, ok := a.allowedTools[name]; ok {
			globs = append(globs, g...)
		}
	}
	return globs
}

// UpstreamToken returns the bearer token for a given upstream by walking the
// caller's roles in config order. The first role that has credentials for the
// upstream wins.
func (a *Authenticator) UpstreamToken(roles []string, upstreamName string) (string, bool) {
	for _, role := range roles {
		if env, ok := a.credentials[role]; ok {
			if token, ok := env[upstreamName]; ok {
				return token, true
			}
		}
	}
	return "", false
}

// orderByConfig returns the given roles sorted by their order in the config.
// Roles not present in the config are excluded.
func (a *Authenticator) orderByConfig(roles []string) []string {
	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}
	var ordered []string
	for _, r := range a.roleOrder {
		if roleSet[r] {
			ordered = append(ordered, r)
		}
	}
	return ordered
}

// Middleware returns HTTP middleware that authenticates requests.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, keyLabel, err := a.Authenticate(r)
		if err != nil {
			writeJSONRPCError(w, -32000, "unauthorized")
			return
		}
		if caller != nil {
			ctx := ContextWithCaller(r.Context(), caller)
			if keyLabel == "oidc" {
				ctx = ContextWithAuthMethod(ctx, "oidc")
			} else {
				ctx = ContextWithAuthMethod(ctx, "apikey")
				ctx = ContextWithKeyLabel(ctx, keyLabel)
			}
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// AdminAuthOption configures AdminAuthMiddleware behavior.
type AdminAuthOption func(*adminAuthConfig)

type adminAuthConfig struct {
	sessionCheck func(*http.Request) bool
}

// WithSessionCheck adds an alternative auth check (e.g. session cookies).
// If the Bearer token is missing or invalid but sessionCheck returns true,
// the request is allowed through.
func WithSessionCheck(fn func(*http.Request) bool) AdminAuthOption {
	return func(c *adminAuthConfig) { c.sessionCheck = fn }
}

// AdminAuthMiddleware returns middleware that protects admin endpoints.
// When devMode is true and no admin key is configured, admin endpoints are open.
// When devMode is false and no admin key is configured, admin endpoints always return 403.
// The login/logout UI routes are always exempt so browsers can authenticate.
func AdminAuthMiddleware(adminKeyHash [32]byte, devMode bool, opts ...AdminAuthOption) func(http.Handler) http.Handler {
	zeroHash := [32]byte{}
	hasAdminKey := subtle.ConstantTimeCompare(adminKeyHash[:], zeroHash[:]) != 1

	var cfg adminAuthConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Login and logout routes are always accessible.
			if r.URL.Path == "/admin/ui/login" || r.URL.Path == "/admin/ui/logout" {
				next.ServeHTTP(w, r)
				return
			}

			if !hasAdminKey {
				if !devMode {
					writeForbidden(w)
					return
				}
				// Dev mode: allow through without auth.
				next.ServeHTTP(w, r)
				return
			}

			// Try Bearer token first.
			header := r.Header.Get("Authorization")
			if strings.HasPrefix(header, "Bearer ") {
				token := strings.TrimPrefix(header, "Bearer ")
				hash := sha256.Sum256([]byte(token))
				if subtle.ConstantTimeCompare(hash[:], adminKeyHash[:]) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Try session cookie if configured.
			if cfg.sessionCheck != nil && cfg.sessionCheck(r) {
				next.ServeHTTP(w, r)
				return
			}

			// For UI routes, redirect to login instead of returning 403.
			if strings.HasPrefix(r.URL.Path, "/admin/ui/") {
				http.Redirect(w, r, "/admin/ui/login", http.StatusFound)
				return
			}

			writeForbidden(w)
		})
	}
}

func writeJSONRPCError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":%d,"message":%q},"id":null}`, code, message)
}

func writeForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"error":"forbidden"}`))
}
