//go:build ignore

// mock-oauth-provider.go — a minimal OAuth 2.0 authorization code flow server for local testing.
//
// Endpoints:
//   GET  /authorize — renders a consent page that auto-submits (or instantly with ?auto=true).
//   POST /token     — exchanges authorization code or refresh token for access tokens.
//
// Usage:
//
//	go run scripts/mock-oauth-provider.go -port 9100
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type pendingCode struct {
	code         string
	redirectURI  string
	codeVerifier string // expected PKCE verifier
	user         string // simulated user
	createdAt    time.Time
}

var (
	mu    sync.Mutex
	codes = map[string]*pendingCode{} // code → pending
)

func main() {
	port := flag.Int("port", 9100, "listen port")
	flag.Parse()

	http.HandleFunc("/authorize", handleAuthorize)
	http.HandleFunc("/token", handleToken)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Mock OAuth provider listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")

	if clientID == "" || redirectURI == "" || state == "" {
		http.Error(w, "missing required parameters (client_id, redirect_uri, state)", http.StatusBadRequest)
		return
	}

	if codeChallenge != "" && codeChallengeMethod != "S256" {
		http.Error(w, "only S256 code challenge method is supported", http.StatusBadRequest)
		return
	}

	// Simulate a user — use state to derive a deterministic but unique user,
	// or default to alice@example.com.
	user := "alice@example.com"

	// Generate authorization code.
	codeBytes := make([]byte, 16)
	rand.Read(codeBytes)
	code := hex.EncodeToString(codeBytes)

	mu.Lock()
	codes[code] = &pendingCode{
		code:         code,
		redirectURI:  redirectURI,
		codeVerifier: codeChallenge, // store challenge, verify against verifier later
		user:         user,
		createdAt:    time.Now(),
	}
	mu.Unlock()

	auto := q.Get("auto") == "true"
	callbackURL := fmt.Sprintf("%s?code=%s&state=%s", redirectURI, code, state)

	if auto {
		http.Redirect(w, r, callbackURL, http.StatusFound)
		return
	}

	// Render a minimal consent page that auto-submits after 1 second.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Mock OAuth - Authorize</title></head>
<body>
<h2>Mock OAuth Provider</h2>
<p>Authorizing as <strong>%s</strong>...</p>
<p>Redirecting in 1 second...</p>
<script>setTimeout(function(){ window.location.href = %q; }, 1000);</script>
<noscript><a href="%s">Click here to continue</a></noscript>
</body>
</html>`, user, callbackURL, callbackURL)
}

func handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	grantType := r.FormValue("grant_type")

	w.Header().Set("Content-Type", "application/json")

	switch grantType {
	case "authorization_code":
		handleAuthCodeExchange(w, r)
	case "refresh_token":
		handleRefreshToken(w, r)
	default:
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "unsupported_grant_type",
			"error_description": fmt.Sprintf("grant_type %q is not supported", grantType),
		})
	}
}

func handleAuthCodeExchange(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	codeVerifier := r.FormValue("code_verifier")

	mu.Lock()
	pc, ok := codes[code]
	if ok {
		delete(codes, code) // single-use
	}
	mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "authorization code not found or already used",
		})
		return
	}

	// Check expiry (5 minute max).
	if time.Since(pc.createdAt) > 5*time.Minute {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "authorization code expired",
		})
		return
	}

	// Verify PKCE if challenge was provided.
	if pc.codeVerifier != "" && codeVerifier != "" {
		hash := sha256.Sum256([]byte(codeVerifier))
		challenge := base64.RawURLEncoding.EncodeToString(hash[:])
		if challenge != pc.codeVerifier {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"error":             "invalid_grant",
				"error_description": "PKCE code_verifier does not match code_challenge",
			})
			return
		}
	}

	// Issue tokens. Access token encodes the user for easy verification.
	accessToken := fmt.Sprintf("mock-token-%s", pc.user)
	refreshToken := fmt.Sprintf("mock-refresh-%s", pc.user)

	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": refreshToken,
		"scope":         "read write",
	})
}

func handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.FormValue("refresh_token")
	if refreshToken == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_request",
			"error_description": "refresh_token is required",
		})
		return
	}

	// For the mock, derive the user from the refresh token format "mock-refresh-<user>".
	user := "unknown"
	const prefix = "mock-refresh-"
	if len(refreshToken) > len(prefix) {
		user = refreshToken[len(prefix):]
	}

	accessToken := fmt.Sprintf("mock-token-%s", user)

	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": refreshToken, // return same refresh token
		"scope":         "read write",
	})
}
