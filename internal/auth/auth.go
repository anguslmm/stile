// Package auth provides inbound authentication, per-caller tool access
// control, and per-caller outbound credential injection.
package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gobwas/glob"

	"github.com/anguslmm/stile/internal/config"
)

// Caller represents an authenticated caller with their access permissions.
type Caller struct {
	Name         string
	AllowedTools []glob.Glob
	AuthEnv      string
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

func contextWithCaller(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// CallerStore looks up callers by API key hash.
type CallerStore interface {
	LookupByKey(hashedKey [32]byte) (*Caller, error)
	HasCallers() (bool, error)
}

// Authenticator handles inbound authentication and credential injection.
type Authenticator struct {
	store    CallerStore
	authEnvs map[string]map[string]string // env name → upstream name → token value
}

// NewAuthenticator creates an Authenticator, resolving auth env config into
// actual token values by reading environment variables.
func NewAuthenticator(store CallerStore, envs []config.AuthEnvConfig) *Authenticator {
	resolved := make(map[string]map[string]string, len(envs))
	for _, env := range envs {
		tokens := make(map[string]string)
		for upstream, envVar := range env.Credentials() {
			val := os.Getenv(envVar)
			if val == "" {
				log.Printf("auth: warning: env var %s not set for auth_env %q upstream %q", envVar, env.Name(), upstream)
				continue
			}
			tokens[upstream] = val
		}
		resolved[env.Name()] = tokens
	}
	return &Authenticator{store: store, authEnvs: resolved}
}

// Authenticate extracts and validates bearer token from the request.
// Returns nil, nil when auth is disabled (no callers and no auth envs).
func (a *Authenticator) Authenticate(r *http.Request) (*Caller, error) {
	hasCallers, err := a.store.HasCallers()
	if err != nil {
		return nil, fmt.Errorf("check callers: %w", err)
	}
	if !hasCallers && len(a.authEnvs) == 0 {
		return nil, nil
	}

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
	return caller, nil
}

// UpstreamToken returns the bearer token for the given upstream within the
// specified auth env. Returns empty string and false if not found.
func (a *Authenticator) UpstreamToken(authEnv, upstreamName string) (string, bool) {
	env, ok := a.authEnvs[authEnv]
	if !ok {
		return "", false
	}
	token, ok := env[upstreamName]
	return token, ok
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
			r = r.WithContext(contextWithCaller(r.Context(), caller))
		}
		next.ServeHTTP(w, r)
	})
}

// AdminAuthMiddleware returns middleware that protects admin endpoints.
// If adminKeyHash is zero (ADMIN_API_KEY not set) and no callers exist, admin
// is open (dev mode). If adminKeyHash is zero but callers exist, admin returns 403.
func AdminAuthMiddleware(adminKeyHash [32]byte, store CallerStore) func(http.Handler) http.Handler {
	zeroHash := [32]byte{}
	hasAdminKey := adminKeyHash != zeroHash

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasAdminKey {
				hasCallers, err := store.HasCallers()
				if err != nil || hasCallers {
					writeForbidden(w)
					return
				}
				// Dev mode: no admin key and no callers — allow through.
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
			if hash != adminKeyHash {
				writeForbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSONRPCError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":%d,"message":%q},"id":null}`, code, message)
}

func writeForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"error":"forbidden"}`))
}
