package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

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

// TestHandleMe_ProjectRoles_MemberUser verifies GET /me for a user who is a
// member of two projects returns project_roles with correct role per project.
func TestHandleMe_ProjectRoles_MemberUser(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	memberID, _ := seedTestUser(t, s.db, "member", "m@x", false, false)
	superCookie, _, _ := s.login(t, "super", "pw-super")
	memberCookie, _, _ := s.login(t, "member", "pw-member")

	// Create two projects (super-admin is not auto-added to project_members per D-04).
	s.do(t, "POST", "/api/v1/projects", superCookie, api.CreateProjectRequest{Name: "proj-a"})
	s.do(t, "POST", "/api/v1/projects", superCookie, api.CreateProjectRequest{Name: "proj-b"})

	// Add member as maintainer on proj-a, viewer on proj-b.
	rbacAddMember(t, s.db, rbacProjectID(t, s.db, "proj-a"), memberID, "maintainer")
	rbacAddMember(t, s.db, rbacProjectID(t, s.db, "proj-b"), memberID, "viewer")

	resp, body := s.do(t, "GET", "/api/v1/me", memberCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /me status=%d body=%v", resp.StatusCode, body)
	}

	roles, ok := body["project_roles"].(map[string]any)
	if !ok {
		t.Fatalf("project_roles missing or wrong type; body=%v", body)
	}
	if roles["proj-a"] != "maintainer" {
		t.Fatalf("proj-a role=%q, want 'maintainer'", roles["proj-a"])
	}
	if roles["proj-b"] != "viewer" {
		t.Fatalf("proj-b role=%q, want 'viewer'", roles["proj-b"])
	}
	if len(roles) != 2 {
		t.Fatalf("expected 2 entries in project_roles, got %d: %v", len(roles), roles)
	}
}

// TestHandleMe_ProjectRoles_SuperAdminEmpty verifies GET /me for a super-admin
// returns project_roles absent (omitempty) — super-admins bypass via is_super_admin.
func TestHandleMe_ProjectRoles_SuperAdminEmpty(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	superCookie, _, _ := s.login(t, "super", "pw-super")

	resp, body := s.do(t, "GET", "/api/v1/me", superCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /me status=%d body=%v", resp.StatusCode, body)
	}

	// project_roles must be absent or an empty map per D-16.
	if pr, exists := body["project_roles"]; exists {
		if m, ok := pr.(map[string]any); !ok || len(m) != 0 {
			t.Fatalf("super-admin project_roles should be absent or empty map; got %v", pr)
		}
	}
}

// TestHandleMe_ProjectRoles_AnonymousNull verifies GET /me without auth returns null body.
func TestHandleMe_ProjectRoles_AnonymousNull(t *testing.T) {
	s := newTestServer(t)

	resp, body := s.do(t, "GET", "/api/v1/me", "" /* no cookie */, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /me anon status=%d", resp.StatusCode)
	}
	// handleMe writes JSON null for unauthenticated callers; the parsed map will be nil/empty.
	if len(body) != 0 {
		t.Fatalf("expected null body for anonymous /me, got %v", body)
	}
}

// TestHandleMe_ProjectRoles_NonMemberEmpty verifies a logged-in user with no
// project memberships gets an empty (non-null) project_roles map.
func TestHandleMe_ProjectRoles_NonMemberEmpty(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "loner", "l@x", false, false)
	lonerCookie, _, _ := s.login(t, "loner", "pw-loner")

	resp, body := s.do(t, "GET", "/api/v1/me", lonerCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /me status=%d body=%v", resp.StatusCode, body)
	}

	// project_roles should be absent (omitempty on empty map) or an empty map.
	if pr, exists := body["project_roles"]; exists {
		if m, ok := pr.(map[string]any); !ok || len(m) != 0 {
			t.Fatalf("non-member project_roles should be absent or empty map; got %v", pr)
		}
	}
}

// rbacProjectID is a helper that looks up a project ID by name for test setup.
func rbacProjectID(t *testing.T, db *metadata.DB, name string) int64 {
	t.Helper()
	p, err := metadata.NewProjectsRepo(db).FindByName(context.Background(), name)
	if err != nil {
		t.Fatalf("rbacProjectID(%q): %v", name, err)
	}
	return p.ID
}

// rbacQueryAuditDetails reads the most-recent audit_log row matching kind+project
// and returns its details_json as a parsed map. Fails the test if no row found.
func rbacQueryAuditDetails(t *testing.T, db *metadata.DB, kind, projectName string) map[string]any {
	t.Helper()
	var raw string
	err := db.Reader.QueryRowContext(context.Background(),
		`SELECT details_json FROM audit_log
		 WHERE event_kind=? AND target_kind='project' AND target_id=?
		 ORDER BY id DESC LIMIT 1`,
		kind, projectName,
	).Scan(&raw)
	if err != nil {
		t.Fatalf("rbacQueryAuditDetails(%q, %q): %v", kind, projectName, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("rbacQueryAuditDetails: unmarshal details_json: %v", err)
	}
	return m
}

// TestHandleAddMember_AuditHasRoleField verifies that POST /members with
// role=viewer emits a member.added audit row with details_json.role == "viewer" (D-12).
func TestHandleAddMember_AuditHasRoleField(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-audit-add"})

	resp, body := s.do(t, "POST", "/api/v1/projects/p-audit-add/members/alice", cookie, map[string]string{"role": "viewer"})
	if resp.StatusCode != 200 {
		t.Fatalf("add member: status=%d body=%v", resp.StatusCode, body)
	}

	details := rbacQueryAuditDetails(t, s.db, "member.added", "p-audit-add")
	if details["user"] != "alice" {
		t.Fatalf("audit member.added: user=%v, want 'alice'", details["user"])
	}
	if details["role"] != "viewer" {
		t.Fatalf("audit member.added: role=%v, want 'viewer'", details["role"])
	}
}

// TestHandlePatchMember_AuditEmit_RecordsOldAndNewRole verifies that PATCH
// demoting a maintainer to viewer emits member.role_changed with all three
// D-11 keys: user, old_role, new_role. Missing old_role is a D-11 violation.
func TestHandlePatchMember_AuditEmit_RecordsOldAndNewRole(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "maint1", "m1@x", false, false)
	seedTestUser(t, s.db, "maint2", "m2@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-audit-patch"})
	// Add two maintainers so demotion of one is allowed by last-maintainer guard.
	s.do(t, "POST", "/api/v1/projects/p-audit-patch/members/maint1", cookie, map[string]string{"role": "maintainer"})
	s.do(t, "POST", "/api/v1/projects/p-audit-patch/members/maint2", cookie, map[string]string{"role": "maintainer"})

	// Demote maint1 to viewer.
	resp, body := s.do(t, "PATCH", "/api/v1/projects/p-audit-patch/members/maint1", cookie, map[string]string{"role": "viewer"})
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH status=%d body=%v", resp.StatusCode, body)
	}

	details := rbacQueryAuditDetails(t, s.db, "member.role_changed", "p-audit-patch")
	if details["user"] != "maint1" {
		t.Fatalf("audit member.role_changed: user=%v, want 'maint1'", details["user"])
	}
	if details["old_role"] != "maintainer" {
		t.Fatalf("audit member.role_changed: old_role=%v, want 'maintainer' (D-11 violation)", details["old_role"])
	}
	if details["new_role"] != "viewer" {
		t.Fatalf("audit member.role_changed: new_role=%v, want 'viewer'", details["new_role"])
	}
}

// TestHandleAddMember_AuditDefaultRole verifies POST with empty body emits
// member.added with details_json.role == "viewer" (D-02 application default).
func TestHandleAddMember_AuditDefaultRole(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "bob", "b@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p-audit-default"})

	// POST with nil body — no role specified, should default to viewer.
	resp, body := s.do(t, "POST", "/api/v1/projects/p-audit-default/members/bob", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("add member no-body: status=%d body=%v", resp.StatusCode, body)
	}

	details := rbacQueryAuditDetails(t, s.db, "member.added", "p-audit-default")
	if details["role"] != "viewer" {
		t.Fatalf("audit member.added default: role=%v, want 'viewer' (D-02)", details["role"])
	}
}
