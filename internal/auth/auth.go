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

type contextKey struct{}

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

// CallerStore looks up callers by API key hash.
type CallerStore interface {
	LookupByKey(hashedKey [32]byte) (*Caller, error)
	RolesForCaller(name string) ([]string, error)
}

// Authenticator handles inbound authentication and credential injection.
type Authenticator struct {
	store        CallerStore
	credentials  map[string]map[string]string // role name → upstream name → token value
	allowedTools map[string][]glob.Glob       // role name → compiled patterns
	roleOrder    []string                     // role names in config order
}

// NewAuthenticator creates an Authenticator, resolving role config into
// actual token values by reading environment variables and compiling glob
// patterns for tool access.
func NewAuthenticator(store CallerStore, roles []config.RoleConfig) *Authenticator {
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

	return &Authenticator{
		store:        store,
		credentials:  creds,
		allowedTools: globs,
		roleOrder:    order,
	}
}

// Authenticate extracts and validates bearer token from the request.
func (a *Authenticator) Authenticate(r *http.Request) (*Caller, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, fmt.Errorf("missing Authorization header")
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, fmt.Errorf("Authorization header must use Bearer scheme")
	}

	token := strings.TrimPrefix(header, "Bearer ")
	hash := sha256.Sum256([]byte(token))

	caller, err := a.store.LookupByKey(hash)
	if err != nil {
		return nil, fmt.Errorf("unauthorized")
	}

	// Get all roles assigned to this caller and order by config order.
	roles, err := a.store.RolesForCaller(caller.Name)
	if err != nil {
		return nil, fmt.Errorf("lookup roles: %w", err)
	}
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
		caller, err := a.Authenticate(r)
		if err != nil {
			writeJSONRPCError(w, -32000, "unauthorized")
			return
		}
		if caller != nil {
			r = r.WithContext(ContextWithCaller(r.Context(), caller))
		}
		next.ServeHTTP(w, r)
	})
}

// AdminAuthMiddleware returns middleware that protects admin endpoints.
// When devMode is true and no admin key is configured, admin endpoints are open.
// When devMode is false and no admin key is configured, admin endpoints always return 403.
func AdminAuthMiddleware(adminKeyHash [32]byte, devMode bool) func(http.Handler) http.Handler {
	zeroHash := [32]byte{}
	hasAdminKey := subtle.ConstantTimeCompare(adminKeyHash[:], zeroHash[:]) != 1

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasAdminKey {
				if !devMode {
					writeForbidden(w)
					return
				}
				// Dev mode: allow through without auth.
				next.ServeHTTP(w, r)
				return
			}

			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				writeForbidden(w)
				return
			}
			token := strings.TrimPrefix(header, "Bearer ")
			hash := sha256.Sum256([]byte(token))
			if subtle.ConstantTimeCompare(hash[:], adminKeyHash[:]) != 1 {
				writeForbidden(w)
				return
			}
			next.ServeHTTP(w, r)
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
