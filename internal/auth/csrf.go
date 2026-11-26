package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// CSRF issues and verifies signed tokens. A token is bound to the current
// session (or to an anonymous nonce cookie for login/setup forms), so a token
// obtained by an attacker is useless against another browser.
type CSRF struct {
	secret []byte
}

// CSRFCookieName is the cookie that carries the anonymous nonce.
const CSRFCookieName = "dt_csrf"

// NewCSRF creates a CSRF manager from the application secret.
func NewCSRF(secret string) *CSRF {
	return &CSRF{secret: []byte(secret)}
}

// NewNonce returns a fresh random nonce.
func (c *CSRF) NewNonce() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is not recoverable; the process cannot be trusted.
		panic("docktimer: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Token returns a signed token for the given nonce and session.
func (c *CSRF) Token(nonce, sessionID string) string {
	return nonce + "." + base64.RawURLEncoding.EncodeToString(c.mac(nonce, sessionID))
}

// Validate checks the signature, the nonce binding and the session binding.
func (c *CSRF) Validate(token, nonce, sessionID string) bool {
	if token == "" || nonce == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] != nonce {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	return hmac.Equal(got, c.mac(nonce, sessionID))
}

func (c *CSRF) mac(nonce, sessionID string) []byte {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte("csrf|"))
	m.Write([]byte(nonce))
	m.Write([]byte("|"))
	m.Write([]byte(sessionID))
	return m.Sum(nil)
}
