package metadata_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// seedDeletedUser creates a user and soft-deletes it (deleted_at != NULL).
func seedDeletedUser(t *testing.T, db *metadata.DB, login string) int64 {
	t.Helper()
	uid := seedUser(t, db, login)
	err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE users SET deleted_at=? WHERE id=?`, time.Now().UTC(), uid)
		return err
	})
	if err != nil {
		t.Fatalf("seedDeletedUser: %v", err)
	}
	return uid
}

// TestMembersRepo_AddWithRole verifies Add stores the provided role.
func TestMembersRepo_AddWithRole(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "proj-add-role")
	uid := seedUser(t, db, "user-add-role")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, pid, uid, "viewer"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Read back the role directly from the DB.
	var got string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT role FROM project_members WHERE project_id=? AND user_id=?`, pid, uid,
	).Scan(&got); err != nil {
		t.Fatalf("scan role: %v", err)
	}
	if got != "viewer" {
		t.Fatalf("role = %q, want %q", got, "viewer")
	}
}

// TestMembersRepo_AddRejectsInvalidRole verifies Add rejects role='owner'.
func TestMembersRepo_AddRejectsInvalidRole(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "proj-reject-role")
	uid := seedUser(t, db, "user-reject-role")

	m := metadata.NewMembersRepo(db)
	err := m.Add(ctx, pid, uid, "owner")
	if err == nil {
		t.Fatal("expected error for role='owner', got nil")
	}
}

// TestMembersRepo_UpdateRole_HappyPath verifies UpdateRole changes the stored role.
func TestMembersRepo_UpdateRole_HappyPath(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "proj-update-role")
	uid := seedUser(t, db, "user-update-role")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, pid, uid, "viewer"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := m.UpdateRole(ctx, pid, uid, "maintainer"); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}

	var got string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT role FROM project_members WHERE project_id=? AND user_id=?`, pid, uid,
	).Scan(&got); err != nil {
		t.Fatalf("scan role: %v", err)
	}
	if got != "maintainer" {
		t.Fatalf("role = %q, want maintainer", got)
	}
}

// TestMembersRepo_UpdateRole_NotFound verifies UpdateRole returns an error for missing rows.
func TestMembersRepo_UpdateRole_NotFound(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	m := metadata.NewMembersRepo(db)
	err := m.UpdateRole(ctx, 999, 999, "maintainer")
	if err == nil {
		t.Fatal("expected error for missing row, got nil")
	}
	if !containsAny(err.Error(), "not found") {
		t.Fatalf("error %q does not mention 'not found'", err.Error())
	}
}

// TestMembersRepo_GetRole_Found verifies GetRole returns the stored role for a member.
func TestMembersRepo_GetRole_Found(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "proj-getrole-found")
	uid := seedUser(t, db, "user-getrole-found")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, pid, uid, "maintainer"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	role, found := m.GetRole(ctx, pid, uid)
	if !found {
		t.Fatal("GetRole: found=false, want true")
	}
	if role != "maintainer" {
		t.Fatalf("GetRole role=%q, want 'maintainer'", role)
	}
}

// TestMembersRepo_GetRole_NotFound verifies GetRole returns ("", false) for absent rows.
func TestMembersRepo_GetRole_NotFound(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	m := metadata.NewMembersRepo(db)
	role, found := m.GetRole(ctx, 9999, 9999)
	if found {
		t.Fatalf("GetRole: found=true for absent row, role=%q", role)
	}
	if role != "" {
		t.Fatalf("GetRole role=%q, want empty string for absent row", role)
	}
}

// TestMembersRepo_CountMaintainers_Zero verifies CountMaintainers returns 0 for only viewers.
func TestMembersRepo_CountMaintainers_Zero(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "proj-count-zero")
	uid := seedUser(t, db, "user-count-zero")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, pid, uid, "viewer"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	n, err := m.CountMaintainers(ctx, pid)
	if err != nil {
		t.Fatalf("CountMaintainers: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountMaintainers = %d, want 0", n)
	}
}

// TestMembersRepo_CountMaintainers_One verifies CountMaintainers counts correctly.
func TestMembersRepo_CountMaintainers_One(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "proj-count-one")
	uid1 := seedUser(t, db, "user-count-one-m")
	uid2 := seedUser(t, db, "user-count-one-v1")
	uid3 := seedUser(t, db, "user-count-one-v2")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, pid, uid1, "maintainer"); err != nil {
		t.Fatalf("Add maintainer: %v", err)
	}
	if err := m.Add(ctx, pid, uid2, "viewer"); err != nil {
		t.Fatalf("Add viewer1: %v", err)
	}
	if err := m.Add(ctx, pid, uid3, "viewer"); err != nil {
		t.Fatalf("Add viewer2: %v", err)
	}

	n, err := m.CountMaintainers(ctx, pid)
	if err != nil {
		t.Fatalf("CountMaintainers: %v", err)
	}
	if n != 1 {
		t.Fatalf("CountMaintainers = %d, want 1", n)
	}
}

// TestMembersRepo_CountMaintainers_ExcludesDeletedUsers verifies soft-deleted users are excluded.
func TestMembersRepo_CountMaintainers_ExcludesDeletedUsers(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "proj-count-deleted")
	uid := seedDeletedUser(t, db, "user-count-deleted-m")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, pid, uid, "maintainer"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	n, err := m.CountMaintainers(ctx, pid)
	if err != nil {
		t.Fatalf("CountMaintainers: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountMaintainers = %d, want 0 (deleted user excluded)", n)
	}
}

// TestMembersRepo_ListProjectRolesForUser_Basic verifies the map has correct entries.
func TestMembersRepo_ListProjectRolesForUser_Basic(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	p1 := seedProject(t, db, "proj-roles-basic-1")
	p2 := seedProject(t, db, "proj-roles-basic-2")
	uid := seedUser(t, db, "user-roles-basic")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, p1, uid, "maintainer"); err != nil {
		t.Fatalf("Add p1: %v", err)
	}
	if err := m.Add(ctx, p2, uid, "viewer"); err != nil {
		t.Fatalf("Add p2: %v", err)
	}

	roles, err := m.ListProjectRolesForUser(ctx, uid)
	if err != nil {
		t.Fatalf("ListProjectRolesForUser: %v", err)
	}
	if len(roles) != 2 {
		t.Fatalf("len(roles) = %d, want 2", len(roles))
	}
	if roles[p1] != "maintainer" {
		t.Fatalf("p1 role = %q, want 'maintainer'", roles[p1])
	}
	if roles[p2] != "viewer" {
		t.Fatalf("p2 role = %q, want 'viewer'", roles[p2])
	}
}

// TestMembersRepo_ListProjectRolesForUser_ExcludesDeletedProject verifies deleted projects are excluded.
func TestMembersRepo_ListProjectRolesForUser_ExcludesDeletedProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	p1 := seedProject(t, db, "proj-roles-excldeleted-live")
	p2 := seedProject(t, db, "proj-roles-excldeleted-dead")
	uid := seedUser(t, db, "user-roles-excldeleted")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, p1, uid, "maintainer"); err != nil {
		t.Fatalf("Add p1: %v", err)
	}
	if err := m.Add(ctx, p2, uid, "viewer"); err != nil {
		t.Fatalf("Add p2: %v", err)
	}

	// Soft-delete p2.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET deleted_at=? WHERE id=?`, time.Now().UTC(), p2)
		return err
	}); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	roles, err := m.ListProjectRolesForUser(ctx, uid)
	if err != nil {
		t.Fatalf("ListProjectRolesForUser: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("len(roles) = %d, want 1 (deleted project excluded)", len(roles))
	}
	if roles[p1] != "maintainer" {
		t.Fatalf("p1 role = %q, want 'maintainer'", roles[p1])
	}
}

// TestMembersRepo_ListProjectRolesForUser_Empty verifies empty map (not nil) for users with no memberships.
func TestMembersRepo_ListProjectRolesForUser_Empty(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	uid := seedUser(t, db, "user-roles-empty")

	m := metadata.NewMembersRepo(db)
	roles, err := m.ListProjectRolesForUser(ctx, uid)
	if err != nil {
		t.Fatalf("ListProjectRolesForUser: %v", err)
	}
	if roles == nil {
		t.Fatal("ListProjectRolesForUser: got nil map, want non-nil empty map")
	}
	if len(roles) != 0 {
		t.Fatalf("len(roles) = %d, want 0", len(roles))
	}
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
