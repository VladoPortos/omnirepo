package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func seedProject(t *testing.T, db *metadata.DB, name string) int64 {
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
		t.Fatalf("seedProject: %v", err)
	}
	return id
}

func TestAPIKeysRepo_CreateUserKeyAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateUserKey(ctx, uid, "dev-laptop", "abcdefgh", "sha256hex")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	k, err := repo.FindByPrefixSha(ctx, "abcdefgh", "sha256hex")
	if err != nil {
		t.Fatalf("FindByPrefixSha: %v", err)
	}
	if k.ID != id || k.OwnerKind != "user" || k.OwnerUserID == nil || *k.OwnerUserID != uid {
		t.Fatalf("unexpected %+v", k)
	}
	if k.OwnerProjectID != nil {
		t.Fatalf("project id should be nil for user key")
	}
}

func TestAPIKeysRepo_CreateProjectKeyAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	pid := seedProject(t, db, "acme")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateProjectKey(ctx, pid, "ci", "12345678", "shaP")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	k, err := repo.FindByPrefixSha(ctx, "12345678", "shaP")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if k.ID != id || k.OwnerKind != "project" || k.OwnerProjectID == nil || *k.OwnerProjectID != pid {
		t.Fatalf("unexpected %+v", k)
	}
}

func TestAPIKeysRepo_TouchLastUsed(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateUserKey(ctx, uid, "k", "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.TouchLastUsed(ctx, id, now); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	k, err := repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if k.LastUsedAt == nil || !k.LastUsedAt.Equal(now) {
		t.Fatalf("LastUsedAt: %v, want %v", k.LastUsedAt, now)
	}
}

func TestAPIKeysRepo_RevokeExcludesFromFind(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewAPIKeysRepo(db)
	ctx := context.Background()
	id, err := repo.CreateUserKey(ctx, uid, "k", "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}
	if err := repo.Revoke(ctx, id); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err = repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-revoke: %v, want ErrNotFound", err)
	}
}
