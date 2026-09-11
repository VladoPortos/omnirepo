package metadata_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestUsersRepo_CreateAndFindByLogin(t *testing.T) {
	db := sqlitetest.New(t)
	repo := metadata.NewUsersRepo(db)
	ctx := context.Background()
	id, err := repo.Create(ctx, "alice", "alice@example.com", "hash", false, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id: %d", id)
	}
	u, err := repo.FindByLogin(ctx, "alice")
	if err != nil {
		t.Fatalf("FindByLogin: %v", err)
	}
	if u.ID != id || u.Login != "alice" || u.Email != "alice@example.com" {
		t.Fatalf("unexpected user %+v", u)
	}
	if !u.MustChangePassword {
		t.Fatalf("MustChangePassword: false, want true")
	}
	if u.IsSuperAdmin {
		t.Fatalf("IsSuperAdmin: true, want false")
	}
}

func TestUsersRepo_ApplyAdminPatchIsAtomicAndProtectsLastSuperAdmin(t *testing.T) {
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	sessions := metadata.NewSessionsRepo(db)
	ctx := context.Background()
	rootID, err := users.Create(ctx, "root", "old@example.com", "old-hash", true, false)
	if err != nil {
		t.Fatal(err)
	}

	newEmail := "new@example.com"
	demote := false
	err = users.ApplyAdminPatch(ctx, rootID, metadata.AdminUserPatch{
		Email: &newEmail, IsSuperAdmin: &demote,
	})
	if !errors.Is(err, metadata.ErrLastSuperAdmin) {
		t.Fatalf("ApplyAdminPatch=%v want ErrLastSuperAdmin", err)
	}
	root, err := users.FindByID(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Email != "old@example.com" || !root.IsSuperAdmin {
		t.Fatalf("rejected patch partially applied: %+v", root)
	}

	if _, err := users.Create(ctx, "root2", "root2@example.com", "hash", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Create(ctx, rootID, "prefix", "sha", time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	mustChange := true
	newHash := "new-hash"
	err = users.ApplyAdminPatch(ctx, rootID, metadata.AdminUserPatch{
		Email: &newEmail, IsSuperAdmin: &demote, MustChangePassword: &mustChange, PasswordHash: &newHash,
	})
	if err != nil {
		t.Fatalf("ApplyAdminPatch: %v", err)
	}
	root, err = users.FindByID(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Email != newEmail || root.IsSuperAdmin || !root.MustChangePassword || root.PasswordHash != newHash || root.PasswordChangedAt == nil {
		t.Fatalf("patch not fully applied: %+v", root)
	}
	var sessionCount int
	if err := db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, rootID).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 {
		t.Fatalf("session count=%d want 0", sessionCount)
	}
}

func TestUsersRepo_DuplicateLoginRejected(t *testing.T) {
	db := sqlitetest.New(t)
	repo := metadata.NewUsersRepo(db)
	ctx := context.Background()
	if _, err := repo.Create(ctx, "alice", "a@x", "h", false, false); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := repo.Create(ctx, "alice", "b@x", "h", false, false)
	if err == nil {
		t.Fatalf("duplicate Create: err=nil, want UNIQUE constraint error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Fatalf("duplicate error message does not mention unique/constraint: %v", err)
	}
}

func TestUsersRepo_SetMustChangePasswordAndUpdatePasswordHash(t *testing.T) {
	db := sqlitetest.New(t)
	repo := metadata.NewUsersRepo(db)
	ctx := context.Background()
	id, err := repo.Create(ctx, "alice", "a@x", "oldhash", false, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.SetMustChangePassword(ctx, id, false); err != nil {
		t.Fatalf("SetMustChangePassword: %v", err)
	}
	u, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if u.MustChangePassword {
		t.Fatalf("MustChangePassword still true after set false")
	}
	if err := repo.UpdatePasswordHash(ctx, id, "newhash"); err != nil {
		t.Fatalf("UpdatePasswordHash: %v", err)
	}
	u, err = repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID2: %v", err)
	}
	if u.PasswordHash != "newhash" {
		t.Fatalf("hash: %q, want newhash", u.PasswordHash)
	}
	if u.PasswordChangedAt == nil {
		t.Fatalf("PasswordChangedAt: nil, want set")
	}
}

func TestUsersRepo_DeleteSoftDeletes(t *testing.T) {
	db := sqlitetest.New(t)
	repo := metadata.NewUsersRepo(db)
	ctx := context.Background()
	id, err := repo.Create(ctx, "alice", "a@x", "h", false, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = repo.FindByLogin(ctx, "alice")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("FindByLogin post-delete: %v, want ErrNotFound", err)
	}
	_, err = repo.FindByID(ctx, id)
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("FindByID post-delete: %v, want ErrNotFound", err)
	}
}

func TestUsersRepo_SetIsSuperAdmin(t *testing.T) {
	db := sqlitetest.New(t)
	repo := metadata.NewUsersRepo(db)
	ctx := context.Background()
	id, err := repo.Create(ctx, "alice", "a@x", "h", false, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.SetIsSuperAdmin(ctx, id, true); err != nil {
		t.Fatalf("SetIsSuperAdmin: %v", err)
	}
	u, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if !u.IsSuperAdmin {
		t.Fatalf("IsSuperAdmin: false, want true")
	}
}

func TestUsersRepo_FindByLogin_NotFound(t *testing.T) {
	db := sqlitetest.New(t)
	repo := metadata.NewUsersRepo(db)
	_, err := repo.FindByLogin(context.Background(), "nobody")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestUsersRepo_DeleteEnforceLastSuperAdmin pins the transactional guard
// that prevents the admin-delete handler from soft-deleting the last
// live super-admin. The check and the soft-delete share one
// WriteTx so two concurrent delete requests cannot race past the count
// and both succeed.
func TestUsersRepo_DeleteEnforceLastSuperAdmin(t *testing.T) {
	db := sqlitetest.New(t)
	repo := metadata.NewUsersRepo(db)
	ctx := context.Background()

	// Two live super-admins — first delete should succeed.
	sa1, err := repo.Create(ctx, "super1", "s1@x", "h", true, false)
	if err != nil {
		t.Fatalf("Create super1: %v", err)
	}
	sa2, err := repo.Create(ctx, "super2", "s2@x", "h", true, false)
	if err != nil {
		t.Fatalf("Create super2: %v", err)
	}
	if err := repo.DeleteEnforceLastSuperAdmin(ctx, sa1); err != nil {
		t.Fatalf("first delete: %v", err)
	}

	// sa2 is now the only live super-admin — delete must be refused.
	err = repo.DeleteEnforceLastSuperAdmin(ctx, sa2)
	if !errors.Is(err, metadata.ErrLastSuperAdmin) {
		t.Fatalf("last super-admin delete: %v, want ErrLastSuperAdmin", err)
	}

	// sa2 still live.
	u, err := repo.FindByID(ctx, sa2)
	if err != nil {
		t.Fatalf("FindByID sa2: %v", err)
	}
	if !u.IsSuperAdmin {
		t.Fatal("sa2 should still be a super-admin")
	}

	// Non-super-admin users still delete freely.
	uid, err := repo.Create(ctx, "alice", "a@x", "h", false, false)
	if err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	if err := repo.DeleteEnforceLastSuperAdmin(ctx, uid); err != nil {
		t.Fatalf("delete non-admin: %v", err)
	}
}
