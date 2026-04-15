package metadata_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
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
