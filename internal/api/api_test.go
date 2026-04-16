package api_test

// TestAPI_GoldenPath is a comprehensive integration test exercising every
// REST endpoint group against a running test server. Individual endpoint
// tests exist in dedicated *_test.go files; this test proves the full
// product works end-to-end in a single logical flow.
//
// Flow: auth -> projects -> members -> repos -> search -> admin audit ->
// admin maintenance -> admin trash -> admin users -> admin trivy ->
// admin tls -> admin gc -> profile -> dashboard.

import (
	"fmt"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/api"
)

func TestAPI_Auth(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)

	// Login.
	cookie, resp, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}
	if cookie == "" || resp.Login != "root" {
		t.Fatalf("unexpected login response: cookie=%q resp=%+v", cookie, resp)
	}

	// Change password.
	r, _ := s.do(t, "POST", "/api/v1/auth/change-password", cookie, api.ChangePasswordRequest{
		Current: pw,
		New:     "newpassword123",
	})
	if r.StatusCode != 200 {
		t.Fatalf("change-password code=%d", r.StatusCode)
	}

	// Re-login with new password.
	cookie, _, code = s.login(t, "root", "newpassword123")
	if code != 200 {
		t.Fatalf("re-login code=%d", code)
	}

	// Logout.
	r, _ = s.do(t, "POST", "/api/v1/auth/logout", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("logout code=%d", r.StatusCode)
	}

	// Stale cookie returns 200 null (not authenticated).
	r, b := s.do(t, "GET", "/api/v1/me", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("stale cookie should 200 null, got %d", r.StatusCode)
	}
	if b != nil && b["login"] != nil {
		t.Fatalf("stale cookie should return null body, got %+v", b)
	}
}

func TestAPI_Projects(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Create project.
	r, body := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "testproj"})
	if r.StatusCode != 200 {
		t.Fatalf("create project code=%d body=%+v", r.StatusCode, body)
	}

	// List projects.
	r, body = s.do(t, "GET", "/api/v1/projects", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("list projects code=%d", r.StatusCode)
	}

	// Get project.
	r, body = s.do(t, "GET", "/api/v1/projects/testproj", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("get project code=%d", r.StatusCode)
	}
	if body["name"] != "testproj" {
		t.Fatalf("project name=%v want testproj", body["name"])
	}

	// Delete project.
	r, _ = s.do(t, "DELETE", "/api/v1/projects/testproj", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("delete project code=%d", r.StatusCode)
	}

	// Get deleted project returns 404.
	r, _ = s.do(t, "GET", "/api/v1/projects/testproj", cookie, nil)
	if r.StatusCode != 404 {
		t.Fatalf("deleted project should 404, got %d", r.StatusCode)
	}
}

func TestAPI_Members(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	seedTestUser(t, s.db, "alice", "alice@x", false, false)
	cookie, _, _ := s.login(t, "root", pw)

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "mp"})

	// Add member.
	r, _ := s.do(t, "POST", "/api/v1/projects/mp/members/alice", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("add member code=%d", r.StatusCode)
	}

	// Remove member.
	r, _ = s.do(t, "DELETE", "/api/v1/projects/mp/members/alice", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("remove member code=%d", r.StatusCode)
	}
}

func TestAPI_Repos(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "rp"})

	// Create repos of each type.
	types := []string{"rpm", "deb", "pypi", "docker", "helm", "git", "raw"}
	for _, typ := range types {
		r, body := s.do(t, "POST", "/api/v1/projects/rp/repos", cookie, api.CreateRepoRequest{
			Name: "r-" + typ,
			Type: typ,
		})
		if r.StatusCode != 200 {
			t.Fatalf("create %s repo code=%d body=%+v", typ, r.StatusCode, body)
		}
	}

	// List repos.
	r, body := s.do(t, "GET", "/api/v1/projects/rp/repos", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("list repos code=%d", r.StatusCode)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != len(types) {
		t.Fatalf("expected %d repos, got %d items=%+v", len(types), len(items), body)
	}

	// Get a single repo — regression test for the missing GET handler that
	// made every repo detail page in the UI render Page Not Found.
	r, body = s.do(t, "GET", "/api/v1/projects/rp/repos/docker/r-docker", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("get repo code=%d body=%+v", r.StatusCode, body)
	}
	if name, _ := body["name"].(string); name != "r-docker" {
		t.Fatalf("get repo name mismatch: %+v", body)
	}
	if typ, _ := body["type"].(string); typ != "docker" {
		t.Fatalf("get repo type mismatch: %+v", body)
	}
	// Unknown repo returns 404.
	r, _ = s.do(t, "GET", "/api/v1/projects/rp/repos/docker/nope", cookie, nil)
	if r.StatusCode != 404 {
		t.Fatalf("get unknown repo code=%d (want 404)", r.StatusCode)
	}

	// Patch a repo (e.g. description).
	r, _ = s.do(t, "PATCH", "/api/v1/projects/rp/repos/raw/r-raw", cookie, map[string]any{
		"description": "updated desc",
	})
	if r.StatusCode != 200 {
		t.Fatalf("patch repo code=%d", r.StatusCode)
	}

	// Delete a repo.
	r, _ = s.do(t, "DELETE", "/api/v1/projects/rp/repos/raw/r-raw", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("delete repo code=%d", r.StatusCode)
	}

	// Wipe a repo.
	r, _ = s.do(t, "POST", "/api/v1/projects/rp/repos/docker/r-docker/wipe", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("wipe repo code=%d", r.StatusCode)
	}
}

func TestAPI_Search(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Create searchable data.
	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "searchable"})
	s.do(t, "POST", "/api/v1/projects/searchable/repos", cookie, api.CreateRepoRequest{Name: "repo1", Type: "raw"})

	// Search with query.
	r, body := s.do(t, "GET", "/api/v1/search?q=searchable", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("search code=%d", r.StatusCode)
	}
	// Response should have items array.
	if _, ok := body["items"]; !ok {
		t.Fatalf("search response missing items: %+v", body)
	}

	// Search with kind filter.
	r, _ = s.do(t, "GET", "/api/v1/search?q=repo1&kind=repo", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("search with filter code=%d", r.StatusCode)
	}
}

func TestAPI_AdminAudit(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Generate an audit event.
	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "auditproj"})

	// List audit events.
	r, body := s.do(t, "GET", "/api/v1/admin/audit", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("audit list code=%d", r.StatusCode)
	}
	if _, ok := body["items"]; !ok {
		t.Fatalf("audit response missing items: %+v", body)
	}

	// Filter by actor.
	r, _ = s.do(t, "GET", "/api/v1/admin/audit?actor=root", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("audit filter code=%d", r.StatusCode)
	}

	// Pagination (cursor-based).
	r, _ = s.do(t, "GET", "/api/v1/admin/audit?limit=1", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("audit pagination code=%d", r.StatusCode)
	}
}

func TestAPI_AdminMaintenance(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Get default state.
	r, body := s.do(t, "GET", "/api/v1/admin/maintenance", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("get maintenance code=%d", r.StatusCode)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false, got %v", body["enabled"])
	}

	// Toggle on.
	r, body = s.do(t, "POST", "/api/v1/admin/maintenance", cookie, map[string]any{"enabled": true})
	if r.StatusCode != 200 {
		t.Fatalf("toggle on code=%d", r.StatusCode)
	}
	if body["enabled"] != true {
		t.Fatalf("expected enabled=true after toggle on, got %v", body["enabled"])
	}

	// Toggle off.
	r, body = s.do(t, "POST", "/api/v1/admin/maintenance", cookie, map[string]any{"enabled": false})
	if r.StatusCode != 200 {
		t.Fatalf("toggle off code=%d", r.StatusCode)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false after toggle off, got %v", body["enabled"])
	}
}

func TestAPI_AdminTrash(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// List trash (empty).
	r, body := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("list trash code=%d", r.StatusCode)
	}
	if _, ok := body["items"]; !ok {
		t.Fatalf("trash response missing items: %+v", body)
	}
}

func TestAPI_AdminTrivy(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Get Trivy DB status.
	r, _ := s.do(t, "GET", "/api/v1/admin/trivy/db/status", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("trivy db status code=%d", r.StatusCode)
	}
}

func TestAPI_AdminUsers(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// List users.
	r, body := s.do(t, "GET", "/api/v1/admin/users", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("list users code=%d", r.StatusCode)
	}
	if _, ok := body["items"]; !ok {
		t.Fatalf("users response missing items: %+v", body)
	}

	// Create user.
	r, body = s.do(t, "POST", "/api/v1/admin/users", cookie, api.CreateUserRequest{
		Login: "newuser",
		Email: "new@x",
	})
	if r.StatusCode != 200 {
		t.Fatalf("create user code=%d body=%+v", r.StatusCode, body)
	}
	otp, _ := body["one_time_password"].(string)
	if len(otp) < 8 {
		t.Fatalf("OTP too short: %q", otp)
	}

	// Get user.
	r, body = s.do(t, "GET", "/api/v1/admin/users/newuser", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("get user code=%d", r.StatusCode)
	}
	if body["login"] != "newuser" {
		t.Fatalf("user login=%v want newuser", body["login"])
	}

	// Edit user.
	r, _ = s.do(t, "PATCH", "/api/v1/admin/users/newuser", cookie, map[string]any{
		"email": "updated@x",
	})
	if r.StatusCode != 200 {
		t.Fatalf("edit user code=%d", r.StatusCode)
	}

	// Delete user.
	r, _ = s.do(t, "DELETE", "/api/v1/admin/users/newuser", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("delete user code=%d", r.StatusCode)
	}
}

func TestAPI_AdminTLS(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Get current TLS info.
	r, _ := s.do(t, "GET", "/api/v1/admin/tls/current", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("tls current code=%d", r.StatusCode)
	}

	// Get TLS history.
	r, _ = s.do(t, "GET", "/api/v1/admin/tls/history", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("tls history code=%d", r.StatusCode)
	}
}

func TestAPI_AdminGC(t *testing.T) {
	s := newGCRESTServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Trigger GC.
	r, body := s.do(t, "POST", "/api/v1/admin/gc", cookie, nil)
	if r.StatusCode != 202 {
		t.Fatalf("gc trigger code=%d body=%+v", r.StatusCode, body)
	}
}

func TestAPI_Profile(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Get /me.
	r, body := s.do(t, "GET", "/api/v1/me", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("get me code=%d", r.StatusCode)
	}
	if body["login"] != "root" {
		t.Fatalf("me login=%v want root", body["login"])
	}

	// Patch /me.
	r, _ = s.do(t, "PATCH", "/api/v1/me", cookie, map[string]any{
		"email": "new@x",
	})
	if r.StatusCode != 200 {
		t.Fatalf("patch me code=%d", r.StatusCode)
	}

	// API keys: create.
	r, body = s.do(t, "POST", "/api/v1/me/api-keys", cookie, map[string]any{
		"name": "test-key",
	})
	if r.StatusCode != 200 && r.StatusCode != 201 {
		t.Fatalf("create api key code=%d body=%+v", r.StatusCode, body)
	}
	// One-time reveal should include the secret.
	if secret, ok := body["secret"].(string); !ok || secret == "" {
		t.Fatalf("api key create missing secret: %+v", body)
	}

	// API keys: list.
	r, body = s.do(t, "GET", "/api/v1/me/api-keys", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("list api keys code=%d", r.StatusCode)
	}

	// API keys: revoke (need the ID from list).
	if items, ok := body["items"].([]any); ok && len(items) > 0 {
		if item, ok := items[0].(map[string]any); ok {
			if id, ok := item["id"]; ok {
				r, _ = s.do(t, "DELETE", "/api/v1/me/api-keys/"+idStr(id), cookie, nil)
				if r.StatusCode != 200 {
					t.Fatalf("revoke api key code=%d", r.StatusCode)
				}
			}
		}
	}
}

func TestAPI_Dashboard(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "root@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	r, body := s.do(t, "GET", "/api/v1/dashboard", cookie, nil)
	if r.StatusCode != 200 {
		t.Fatalf("dashboard code=%d", r.StatusCode)
	}
	// Response should have storage and count fields.
	for _, key := range []string{"repo_count", "user_count"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("dashboard missing %s: %+v", key, body)
		}
	}
}

// idStr converts a JSON number or string ID to string for URL construction.
func idStr(v any) string {
	switch v := v.(type) {
	case float64:
		return fmt.Sprintf("%d", int64(v))
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}
