package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookieName = "stile_session"
	sessionTTL        = 24 * time.Hour
)

// signSession creates an HMAC-signed session cookie value.
// Format: "<expiry_unix>.<hex_signature>"
// The signature is HMAC-SHA256(admin_key_hash, expiry_unix).
func signSession(adminKeyHash [32]byte) string {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := strconv.FormatInt(exp, 10)

	mac := hmac.New(sha256.New, adminKeyHash[:])
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s.%s", payload, sig)
}

// verifySession checks that a session cookie value has a valid signature
// and has not expired.
func verifySession(value string, adminKeyHash [32]byte) bool {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payload, sigHex := parts[0], parts[1]

	// Verify expiry.
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}

	// Verify signature.
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, adminKeyHash[:])
	mac.Write([]byte(payload))
	return hmac.Equal(mac.Sum(nil), sig)
}

func setSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/admin/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}
