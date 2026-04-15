package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

func TestGenerateSession_Shape(t *testing.T) {
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("GenerateSession: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s.Plaintext)
	if err != nil {
		t.Fatalf("Plaintext not base64url: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("entropy bytes: got %d, want 32", len(raw))
	}
	if s.Prefix != s.Plaintext[:8] {
		t.Fatalf("Prefix mismatch")
	}
	sum := sha256.Sum256([]byte(s.Plaintext))
	if s.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256 mismatch")
	}
}

func TestGenerateSession_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		s, err := auth.GenerateSession()
		if err != nil {
			t.Fatalf("GenerateSession: %v", err)
		}
		seen[s.Plaintext] = struct{}{}
	}
	if len(seen) != 100 {
		t.Fatalf("duplicates: %d distinct out of 100", len(seen))
	}
}

func TestSetSessionCookie_Secure(t *testing.T) {
	w := httptest.NewRecorder()
	auth.SetSessionCookie(w, "tok", true)
	setCookie := w.Result().Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, auth.SessionCookieName+"=tok") {
		t.Fatalf("Set-Cookie missing token: %q", setCookie)
	}
	for _, want := range []string{"HttpOnly", "Secure", "Path=/", "SameSite=Lax"} {
		if !strings.Contains(setCookie, want) {
			t.Errorf("Set-Cookie missing %q: %q", want, setCookie)
		}
	}
}

func TestSetSessionCookie_Insecure(t *testing.T) {
	w := httptest.NewRecorder()
	auth.SetSessionCookie(w, "tok", false)
	setCookie := w.Result().Header.Get("Set-Cookie")
	if strings.Contains(setCookie, "Secure") {
		t.Fatalf("Set-Cookie has Secure when secure=false: %q", setCookie)
	}
}

func TestClearSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	auth.ClearSessionCookie(w)
	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != auth.SessionCookieName {
		t.Fatalf("cookie name: %q", c.Name)
	}
	if c.MaxAge > 0 {
		t.Fatalf("MaxAge: got %d, want <= 0", c.MaxAge)
	}
}

func TestSessionPrefixAndSHA(t *testing.T) {
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("GenerateSession: %v", err)
	}
	p, ok := auth.SessionPrefix(s.Plaintext)
	if !ok {
		t.Fatalf("SessionPrefix ok=false")
	}
	if p != s.Prefix {
		t.Fatalf("prefix mismatch")
	}
	if _, ok := auth.SessionPrefix("x"); ok {
		t.Fatalf("SessionPrefix short should return false")
	}
	if auth.SessionSHA256(s.Plaintext) != s.SHA256 {
		t.Fatalf("SessionSHA256 mismatch")
	}
}

// ensure *http.Cookie is parseable (sanity for our test).
func TestSetSessionCookie_Parseable(t *testing.T) {
	w := httptest.NewRecorder()
	auth.SetSessionCookie(w, "tok", true)
	req := &http.Request{Header: http.Header{"Cookie": w.Result().Header["Set-Cookie"]}}
	c, err := req.Cookie(auth.SessionCookieName)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Value != "tok" {
		t.Fatalf("value %q", c.Value)
	}
}
