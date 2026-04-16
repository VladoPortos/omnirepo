package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

func TestAdminUsersFull_ListUsers(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	seedTestUser(t, s.db, "bob", "b@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/users?limit=10", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %v", body)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 users, got %d", len(items))
	}
}

func TestAdminUsersFull_ListPagination(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	seedTestUser(t, s.db, "bob", "b@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Page 1.
	resp, body := s.do(t, "GET", "/api/v1/admin/users?limit=2", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	cursor, ok := body["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("expected next_cursor, got %v", body["next_cursor"])
	}

	// Page 2.
	resp, body = s.do(t, "GET", "/api/v1/admin/users?limit=2&cursor="+cursor, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page2 code=%d", resp.StatusCode)
	}
	items = body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item on page2, got %d", len(items))
	}
}

func TestAdminUsersFull_GetUser(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/users/alice", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	if body["login"] != "alice" {
		t.Fatalf("expected login=alice, got %v", body["login"])
	}
	if body["email"] != "a@x" {
		t.Fatalf("expected email=a@x, got %v", body["email"])
	}
}

func TestAdminUsersFull_GetUserNotFound(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, _ := s.do(t, "GET", "/api/v1/admin/users/ghost", cookie, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("code=%d want 404", resp.StatusCode)
	}
}

func TestAdminUsersFull_PatchEmail(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	newEmail := "alice-new@example.com"
	resp, body := s.do(t, "PATCH", "/api/v1/admin/users/alice", cookie, map[string]any{
		"email": newEmail,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}

	// Verify email changed in DB.
	u, err := metadata.NewUsersRepo(s.db).FindByLogin(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != newEmail {
		t.Fatalf("email=%q want %q", u.Email, newEmail)
	}

	// Verify audit event.
	var n int
	_ = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind=?`, string(audit.EvtUserUpdated)).Scan(&n)
	if n == 0 {
		t.Fatalf("no user.updated audit event")
	}
}

func TestAdminUsersFull_PatchForcePasswordReset(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Force password reset.
	tr := true
	resp, _ := s.do(t, "PATCH", "/api/v1/admin/users/alice", cookie, map[string]any{
		"must_change_password": tr,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}

	u, _ := metadata.NewUsersRepo(s.db).FindByLogin(context.Background(), "alice")
	if !u.MustChangePassword {
		t.Fatalf("expected must_change_password=true")
	}
}

func TestAdminUsersFull_PatchSuperAdmin(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	tr := true
	resp, _ := s.do(t, "PATCH", "/api/v1/admin/users/alice", cookie, map[string]any{
		"is_super_admin": tr,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}

	u, _ := metadata.NewUsersRepo(s.db).FindByLogin(context.Background(), "alice")
	if !u.IsSuperAdmin {
		t.Fatalf("expected is_super_admin=true")
	}
}

func TestAdminUsersFull_NonSuperAdmin403(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, _ := s.do(t, "GET", "/api/v1/admin/users", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("list code=%d want 403", resp.StatusCode)
	}

	resp, _ = s.do(t, "GET", "/api/v1/admin/users/alice", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("get code=%d want 403", resp.StatusCode)
	}

	resp, _ = s.do(t, "PATCH", "/api/v1/admin/users/alice", cookie, map[string]any{"email": "x"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("patch code=%d want 403", resp.StatusCode)
	}
}
