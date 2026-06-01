package api_test

import (
	"net/http"
	"testing"
)

func TestProfile_PatchEmail(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "old@example.com", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	newEmail := "new@example.com"
	resp, body := s.do(t, "PATCH", "/api/v1/me", cookie, map[string]any{
		"email": newEmail,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	if body["email"] != newEmail {
		t.Fatalf("expected email=%s, got %v", newEmail, body["email"])
	}
}

func TestProfile_PatchAvatarSeed(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "PATCH", "/api/v1/me", cookie, map[string]any{
		"avatar_seed": "my-seed-123",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	if body["avatar_seed"] != "my-seed-123" {
		t.Fatalf("expected avatar_seed=my-seed-123, got %v", body["avatar_seed"])
	}
}

// GET /api/v1/me must return avatar_seed so the UI can reconstruct the
// customized avatar after reload. Regression guard: the PATCH handler set
// the field, but the GET handler omitted it, so reloading the profile page
// silently reverted the avatar to the login-string default.
func TestProfile_GetMeIncludesAvatarSeed(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	if resp, _ := s.do(t, "PATCH", "/api/v1/me", cookie, map[string]any{
		"avatar_seed": "persisted-seed",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("patch code=%d", resp.StatusCode)
	}

	resp, body := s.do(t, "GET", "/api/v1/me", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get code=%d body=%v", resp.StatusCode, body)
	}
	if body["avatar_seed"] != "persisted-seed" {
		t.Fatalf("expected avatar_seed=persisted-seed in GET /me, got %v", body["avatar_seed"])
	}
}

func TestProfile_PatchEmptyEmail(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, _ := s.do(t, "PATCH", "/api/v1/me", cookie, map[string]any{
		"email": "",
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for empty email, got %d", resp.StatusCode)
	}
}
