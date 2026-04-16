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
