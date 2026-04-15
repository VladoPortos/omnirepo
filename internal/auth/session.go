package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
)

// SessionToken carries a freshly-generated opaque session (D-18). Plaintext is
// the cookie value; Prefix+SHA256 is what the server persists in the sessions
// table.
type SessionToken struct {
	Plaintext string // base64url of 32 random bytes — shown to caller at creation only
	Prefix    string // first 8 chars of Plaintext — indexed on sessions.token_prefix
	SHA256    string // hex sha256(Plaintext)
}

// SessionCookieName is the HTTP cookie name emitted by SetSessionCookie. Also
// the name middlewares look up on incoming requests.
const SessionCookieName = "omnirepo_session"

// sessionRandomBytes is the length of the raw entropy that becomes Plaintext.
// 32 bytes → 256 bits of entropy → 43-char base64url string.
const sessionRandomBytes = 32

// sessionPrefixLen is the cookie-value prefix persisted for index lookup.
const sessionPrefixLen = 8

// GenerateSession returns a fresh SessionToken: 32 random bytes base64url-
// encoded (no padding), with the sha256 of the whole plaintext in hex.
func GenerateSession() (SessionToken, error) {
	b := make([]byte, sessionRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return SessionToken{}, fmt.Errorf("auth: generate session: %w", err)
	}
	plain := base64.RawURLEncoding.EncodeToString(b)
	if len(plain) < sessionPrefixLen {
		// 32 random bytes → 43 chars, so this cannot hit in practice. Guard
		// anyway so Prefix slicing never panics if sessionRandomBytes drops.
		return SessionToken{}, fmt.Errorf("auth: generate session: plaintext too short (%d)", len(plain))
	}
	sum := sha256.Sum256([]byte(plain))
	return SessionToken{
		Plaintext: plain,
		Prefix:    plain[:sessionPrefixLen],
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

// SetSessionCookie writes the session cookie to w. secure flips the Secure
// attribute — callers pass r.TLS != nil to keep dev HTTP working while
// production stays https-only.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie emits a cookie with MaxAge=-1 to remove the session
// from the browser. Used by /logout.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionPrefix returns the token's 8-char prefix used for index lookup on
// sessions.token_prefix. Helpers in middleware call this rather than slicing
// manually so the prefix length stays in one place.
func SessionPrefix(token string) (string, bool) {
	if len(token) < sessionPrefixLen {
		return "", false
	}
	return token[:sessionPrefixLen], true
}

// SessionSHA256 returns hex sha256(token). Middlewares call this before
// comparing to sessions.token_sha256 under constant-time semantics (DB
// lookup by prefix then EqualSHA256).
func SessionSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
