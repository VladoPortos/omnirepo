package api_test

// Regression guard for F-03.5 (walkthrough 3 batch 03): the
// OptionalSessionOrAPIKey middleware used on /api/v1/me must 401 when
// the caller explicitly supplies API-key credentials that don't
// validate. Before the fix it silently dropped failed Basic Auth and
// Bearer tokens, returning 200 null — which made "wrong key" and
// "server returned success" look identical to protocol CLIs and masked
// credential probes from audit / rate-limit accounting.
//
// The stale-cookie path intentionally remains a silent 200 null (see
// TestLogout in admin_phase1_test.go): stale cookies are expected in
// a multi-tab browser session and the SPA relies on that shape to
// decide "show the login page".

import (
	"net/http"
	"testing"
)

func TestOptionalMiddleware_BasicAuth_WrongKey_401(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)

	req, err := http.NewRequest("GET", s.ts.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("alice", "omr_u_NotARealKey12345678901234567")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong Basic Auth key: want 401, got %d", resp.StatusCode)
	}
}

// F-03.5 second half: the middleware must 401 even when the Basic
// password is a random string that doesn't match APIKeyRegex. Otherwise
// a CLI using `curl -u alice:letmein` against /api/v1/me sees 200 null
// (identical to "no creds") and quietly thinks the server returned
// success.
func TestOptionalMiddleware_BasicAuth_MalformedKey_401(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)

	req, err := http.NewRequest("GET", s.ts.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("alice", "definitely-not-an-api-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("malformed Basic password: want 401, got %d", resp.StatusCode)
	}
}

func TestOptionalMiddleware_Bearer_WrongKey_401(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)

	req, err := http.NewRequest("GET", s.ts.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer omr_u_NotARealKey12345678901234567")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong Bearer key: want 401, got %d", resp.StatusCode)
	}
}

// F-03.5 Codex-pass tightening: a header like `Authorization: Bearer `
// with empty payload is still an explicit auth attempt and must 401.
// Before the tighten it fell through as anonymous because stripBearer
// returned "" and the guard used to be `bearer != ""`.
func TestOptionalMiddleware_Bearer_EmptyToken_401(t *testing.T) {
	s := newTestServer(t)

	req, err := http.NewRequest("GET", s.ts.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty Bearer: want 401, got %d", resp.StatusCode)
	}
}

// Unrelated non-Bearer Authorization scheme is also an explicit attempt.
func TestOptionalMiddleware_UnknownScheme_401(t *testing.T) {
	s := newTestServer(t)

	req, err := http.NewRequest("GET", s.ts.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Negotiate abcd")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown scheme: want 401, got %d", resp.StatusCode)
	}
}

func TestOptionalMiddleware_NoCreds_200(t *testing.T) {
	s := newTestServer(t)

	req, err := http.NewRequest("GET", s.ts.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no creds: want 200, got %d", resp.StatusCode)
	}
}
