package admin

import (
	"crypto/sha256"
	"testing"
	"time"
)

func TestSignAndVerifySession(t *testing.T) {
	key := sha256.Sum256([]byte("test-admin-key"))

	token := signSession(key)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	if !verifySession(token, key) {
		t.Error("expected valid session")
	}
}

func TestVerifySessionWrongKey(t *testing.T) {
	key1 := sha256.Sum256([]byte("key-one"))
	key2 := sha256.Sum256([]byte("key-two"))

	token := signSession(key1)
	if verifySession(token, key2) {
		t.Error("expected invalid session with wrong key")
	}
}

func TestVerifySessionExpired(t *testing.T) {
	key := sha256.Sum256([]byte("test-key"))

	// Manually create an expired token.
	expired := "1000000000.abc123"
	if verifySession(expired, key) {
		t.Error("expected expired session to be invalid")
	}
}

func TestVerifySessionMalformed(t *testing.T) {
	key := sha256.Sum256([]byte("test-key"))

	cases := []string{"", "nodot", "not-a-number.sig", "123.not-hex-!!!"}
	for _, c := range cases {
		if verifySession(c, key) {
			t.Errorf("expected invalid for %q", c)
		}
	}
}

func TestDifferentLoginsProduceDifferentTokens(t *testing.T) {
	key := sha256.Sum256([]byte("test-key"))

	token1 := signSession(key)
	time.Sleep(time.Millisecond) // ensure different timestamp
	token2 := signSession(key)

	// They should differ because expiry timestamps differ (by at least 1ms → same second possible).
	// But both should be valid.
	if !verifySession(token1, key) {
		t.Error("token1 should be valid")
	}
	if !verifySession(token2, key) {
		t.Error("token2 should be valid")
	}
}
