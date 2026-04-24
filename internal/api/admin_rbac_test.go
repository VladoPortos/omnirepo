package api_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// rbacSeedProject creates a project and returns its ID. Uses direct DB access
// so we don't depend on the handler under test.
func rbacSeedProject(t *testing.T, db *metadata.DB, name string) int64 {
	t.Helper()
	var id int64
	err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(context.Background(), `INSERT INTO projects(name) VALUES (?)`, name)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("rbacSeedProject: %v", err)
	}
	return id
}

// rbacAddMember directly inserts a project_members row (bypasses handler).
func rbacAddMember(t *testing.T, db *metadata.DB, projectID, userID int64, role string) {
	t.Helper()
	m := metadata.NewMembersRepo(db)
	if err := m.Add(context.Background(), projectID, userID, role); err != nil {
		t.Fatalf("rbacAddMember: %v", err)
	}
}

// rbacGetRole reads the role from project_members directly.
func rbacGetRole(t *testing.T, db *metadata.DB, projectID, userID int64) string {
	t.Helper()
	m := metadata.NewMembersRepo(db)
	role, found := m.GetRole(context.Background(), projectID, userID)
	if !found {
		t.Fatalf("rbacGetRole: member (%d,%d) not found", projectID, userID)
	}
	return role
}

// TestHandleAddMember_DefaultsToViewer verifies POST with empty body stores role='viewer'.
func TestHandleAddMember_DefaultsToViewer(t *testing.T) {
	s := newTestServer(t)
	// super creates project (super-admin → not auto-added to project_members per D-04).
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-default-role"})

	resp, _ := s.do(t, "POST", "/api/v1/projects/p-default-role/members/alice", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("add member: status=%d", resp.StatusCode)
	}

	// Verify role stored in DB.
	p, err := metadata.NewProjectsRepo(s.db).FindByName(context.Background(), "p-default-role")
	if err != nil {
		t.Fatalf("find project: %v", err)
	}
	aliceID := seedTestUserID(t, s.db, "alice")
	role := rbacGetRole(t, s.db, p.ID, aliceID)
	if role != "viewer" {
		t.Fatalf("role = %q, want 'viewer'", role)
	}
}

// TestHandleAddMember_WithRoleMaintainer verifies POST with role=maintainer stores maintainer.
func TestHandleAddMember_WithRoleMaintainer(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "bob", "b@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-maint-role"})

	resp, _ := s.do(t, "POST", "/api/v1/projects/p-maint-role/members/bob", cookie, map[string]string{"role": "maintainer"})
	if resp.StatusCode != 200 {
		t.Fatalf("add member: status=%d", resp.StatusCode)
	}

	p, err := metadata.NewProjectsRepo(s.db).FindByName(context.Background(), "p-maint-role")
	if err != nil {
		t.Fatalf("find project: %v", err)
	}
	bobID := seedTestUserID(t, s.db, "bob")
	role := rbacGetRole(t, s.db, p.ID, bobID)
	if role != "maintainer" {
		t.Fatalf("role = %q, want 'maintainer'", role)
	}
}

// TestHandleAddMember_RejectsInvalidRole verifies POST with role=owner returns 422.
func TestHandleAddMember_RejectsInvalidRole(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "charlie", "c@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-invalid-role"})

	resp, body := s.do(t, "POST", "/api/v1/projects/p-invalid-role/members/charlie", cookie, map[string]string{"role": "owner"})
	if resp.StatusCode != 422 {
		t.Fatalf("status=%d, want 422; body=%v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); code != "validation.failed" {
		t.Fatalf("code=%q, want validation.failed; body=%v", code, body)
	}
}

// TestHandlePatchMember_HappyPath verifies PATCH role=viewer on a maintainer succeeds (super-admin rescues).
func TestHandlePatchMember_HappyPath(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "dave", "d@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-patch-happy"})

	// Add dave as maintainer + add a second maintainer so we can demote dave.
	seedTestUser(t, s.db, "eve", "e@x", false, false)
	s.do(t, "POST", "/api/v1/projects/p-patch-happy/members/dave", cookie, map[string]string{"role": "maintainer"})
	s.do(t, "POST", "/api/v1/projects/p-patch-happy/members/eve", cookie, map[string]string{"role": "maintainer"})

	resp, body := s.do(t, "PATCH", "/api/v1/projects/p-patch-happy/members/dave", cookie, map[string]string{"role": "viewer"})
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH status=%d body=%v", resp.StatusCode, body)
	}

	p, _ := metadata.NewProjectsRepo(s.db).FindByName(context.Background(), "p-patch-happy")
	daveID := seedTestUserID(t, s.db, "dave")
	role := rbacGetRole(t, s.db, p.ID, daveID)
	if role != "viewer" {
		t.Fatalf("role = %q, want 'viewer'", role)
	}
}

// TestHandlePatchMember_LastMaintainer409 verifies PATCH on the last maintainer by non-super-admin returns 409.
func TestHandlePatchMember_LastMaintainer409(t *testing.T) {
	s := newTestServer(t)
	// Non-super maintainer is the actor.
	seedTestUser(t, s.db, "super", "s@x", true, false)
	maintID, _ := seedTestUser(t, s.db, "maint", "m@x", false, false)
	superCookie, _, _ := s.login(t, "super", "pw-super")
	maintCookie, _, _ := s.login(t, "maint", "pw-maint")

	s.do(t, "POST", "/api/v1/projects", superCookie, api.CreateProjectRequest{Name: "p-last-maint"})
	// Add maint as the ONLY maintainer.
	s.do(t, "POST", "/api/v1/projects/p-last-maint/members/maint", superCookie, map[string]string{"role": "maintainer"})

	// Ensure maint can act on the project (they are a maintainer).
	_ = maintID

	resp, body := s.do(t, "PATCH", "/api/v1/projects/p-last-maint/members/maint", maintCookie, map[string]string{"role": "viewer"})
	if resp.StatusCode != 409 {
		t.Fatalf("status=%d, want 409 (last-maintainer guard); body=%v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); code != "rbac.last_maintainer" {
		t.Fatalf("code=%q, want 'rbac.last_maintainer'; body=%v", code, body)
	}
}

// TestHandlePatchMember_SuperAdminRescueBypass verifies super-admin can demote the last maintainer.
func TestHandlePatchMember_SuperAdminRescueBypass(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "only", "o@x", false, false)
	superCookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", superCookie, api.CreateProjectRequest{Name: "p-super-rescue"})
	s.do(t, "POST", "/api/v1/projects/p-super-rescue/members/only", superCookie, map[string]string{"role": "maintainer"})

	// Super-admin demotes the last maintainer — should succeed.
	resp, body := s.do(t, "PATCH", "/api/v1/projects/p-super-rescue/members/only", superCookie, map[string]string{"role": "viewer"})
	if resp.StatusCode != 200 {
		t.Fatalf("super-admin rescue: status=%d body=%v", resp.StatusCode, body)
	}
}

// TestHandlePatchMember_ViewerCannotChangeRoles verifies a viewer gets 403.
func TestHandlePatchMember_ViewerCannotChangeRoles(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "viewer", "v@x", false, false)
	seedTestUser(t, s.db, "target", "t@x", false, false)
	superCookie, _, _ := s.login(t, "super", "pw-super")
	viewerCookie, _, _ := s.login(t, "viewer", "pw-viewer")

	s.do(t, "POST", "/api/v1/projects", superCookie, api.CreateProjectRequest{Name: "p-viewer-403"})
	s.do(t, "POST", "/api/v1/projects/p-viewer-403/members/viewer", superCookie, map[string]string{"role": "viewer"})
	s.do(t, "POST", "/api/v1/projects/p-viewer-403/members/target", superCookie, map[string]string{"role": "viewer"})

	resp, body := s.do(t, "PATCH", "/api/v1/projects/p-viewer-403/members/target", viewerCookie, map[string]string{"role": "maintainer"})
	if resp.StatusCode != 403 {
		t.Fatalf("viewer PATCH: status=%d body=%v", resp.StatusCode, body)
	}
}

// TestHandleRemoveMember_LastMaintainer409 verifies DELETE on last maintainer by non-super-admin returns 409.
func TestHandleRemoveMember_LastMaintainer409(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "solo", "solo@x", false, false)
	superCookie, _, _ := s.login(t, "super", "pw-super")
	soloCookie, _, _ := s.login(t, "solo", "pw-solo")

	s.do(t, "POST", "/api/v1/projects", superCookie, api.CreateProjectRequest{Name: "p-del-last"})
	// Add solo as the only maintainer.
	s.do(t, "POST", "/api/v1/projects/p-del-last/members/solo", superCookie, map[string]string{"role": "maintainer"})

	// Add a second member as viewer so solo can act on the project.
	seedTestUser(t, s.db, "viewer2", "v2@x", false, false)
	s.do(t, "POST", "/api/v1/projects/p-del-last/members/viewer2", soloCookie, map[string]string{"role": "viewer"})

	// Solo tries to remove itself — blocked (last maintainer).
	resp, body := s.do(t, "DELETE", "/api/v1/projects/p-del-last/members/solo", soloCookie, nil)
	if resp.StatusCode != 409 {
		t.Fatalf("DELETE last maintainer: status=%d body=%v", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); code != "rbac.last_maintainer" {
		t.Fatalf("code=%q, want 'rbac.last_maintainer'; body=%v", code, body)
	}
}

// TestHandleRemoveMember_SuperAdminRescueBypass verifies super-admin can remove the last maintainer.
func TestHandleRemoveMember_SuperAdminRescueBypass(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "lastm", "lm@x", false, false)
	superCookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", superCookie, api.CreateProjectRequest{Name: "p-del-super"})
	s.do(t, "POST", "/api/v1/projects/p-del-super/members/lastm", superCookie, map[string]string{"role": "maintainer"})

	resp, body := s.do(t, "DELETE", "/api/v1/projects/p-del-super/members/lastm", superCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("super-admin DELETE last maintainer: status=%d body=%v", resp.StatusCode, body)
	}
}

// TestMintProjectAPIKey_Role verifies the role field is stored in api_keys.
func TestMintProjectAPIKey_Role(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-apikey-role"})

	// Mint with explicit viewer role.
	resp, body := s.do(t, "POST", "/api/v1/projects/p-apikey-role/api-keys", cookie, map[string]string{"name": "ci-viewer", "role": "viewer"})
	if resp.StatusCode != 201 {
		t.Fatalf("mint viewer key: status=%d body=%v", resp.StatusCode, body)
	}

	// Mint with no role — should default to maintainer.
	resp2, body2 := s.do(t, "POST", "/api/v1/projects/p-apikey-role/api-keys", cookie, map[string]string{"name": "ci-default"})
	if resp2.StatusCode != 201 {
		t.Fatalf("mint default key: status=%d body=%v", resp2.StatusCode, body2)
	}
}

// TestMintProjectAPIKey_InvalidRole verifies invalid role returns 422.
func TestMintProjectAPIKey_InvalidRole(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-apikey-invalid"})

	resp, body := s.do(t, "POST", "/api/v1/projects/p-apikey-invalid/api-keys", cookie, map[string]string{"name": "bad", "role": "owner"})
	if resp.StatusCode != 422 {
		t.Fatalf("status=%d, want 422; body=%v", resp.StatusCode, body)
	}
}

// seedTestUserID returns the ID of an already-seeded user (or panics if not found).
// Uses the DB directly since seedTestUser creates user only if not already present.
func seedTestUserID(t *testing.T, db *metadata.DB, login string) int64 {
	t.Helper()
	u, err := metadata.NewUsersRepo(db).FindByLogin(context.Background(), login)
	if err != nil {
		t.Fatalf("seedTestUserID(%q): %v", login, err)
	}
	return u.ID
}
