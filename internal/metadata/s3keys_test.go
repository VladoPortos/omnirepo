package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func seedS3Project(t *testing.T, db *metadata.DB) (projectID, userID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('s3proj')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='s3proj'`).Scan(&projectID); err != nil {
		t.Fatalf("find project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO users(login,email,password_hash) VALUES ('s3u','u@e.c','x')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM users WHERE login='s3u'`).Scan(&userID); err != nil {
		t.Fatalf("find user: %v", err)
	}
	return projectID, userID
}

func TestS3KeysInsertAndFindByAKID(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	row := &metadata.S3AccessKey{
		ProjectID:       projectID,
		AccessKeyID:     "AKIATEST00001",
		SecretEnc:       []byte("sealed-bytes-1"),
		Label:           "primary",
		CreatedByUserID: userID,
	}
	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, row)
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("Insert returned zero id")
	}

	got, err := r.FindByAKID(ctx, "AKIATEST00001")
	if err != nil {
		t.Fatalf("FindByAKID: %v", err)
	}
	if got.ID != id || got.Label != "primary" || string(got.SecretEnc) != "sealed-bytes-1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestS3KeysFindByAKID_RevokedCollapsesToNotFound(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-REV-1", SecretEnc: []byte("x"),
			Label: "l", CreatedByUserID: userID,
		})
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Revoke it.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Revoke(ctx, tx, id)
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// FindByAKID must return ErrS3AccessKeyNotFound (same as missing).
	_, err := r.FindByAKID(ctx, "AKIA-REV-1")
	if !errors.Is(err, metadata.ErrS3AccessKeyNotFound) {
		t.Fatalf("revoked lookup: want ErrS3AccessKeyNotFound, got %v", err)
	}
	_, err = r.FindByAKID(ctx, "AKIA-DOES-NOT-EXIST")
	if !errors.Is(err, metadata.ErrS3AccessKeyNotFound) {
		t.Fatalf("missing lookup: want ErrS3AccessKeyNotFound, got %v", err)
	}

	// FindByID still returns the revoked row for admin scopes.
	got, err := r.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.RevokedAt == nil {
		t.Fatalf("FindByID revoked_at should be set: %+v", got)
	}
}

func TestS3KeysListByProject_ExcludesRevoked(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	var revokedID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-LIVE-1", SecretEnc: []byte("x"),
			Label: "l1", CreatedByUserID: userID,
		}); err != nil {
			return err
		}
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-LIVE-2", SecretEnc: []byte("x"),
			Label: "l2", CreatedByUserID: userID,
		}); err != nil {
			return err
		}
		v, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-DEAD-1", SecretEnc: []byte("x"),
			Label: "dead", CreatedByUserID: userID,
		})
		revokedID = v
		return err
	}); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Revoke(ctx, tx, revokedID)
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	list, err := r.ListByProject(ctx, projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 live keys, got %d: %+v", len(list), list)
	}
	for _, k := range list {
		if strings.HasPrefix(k.AccessKeyID, "AKIA-DEAD") {
			t.Fatalf("revoked key leaked: %+v", k)
		}
	}
}

func TestS3KeysTouchLastUsed(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-TOUCH-1", SecretEnc: []byte("x"),
			Label: "t", CreatedByUserID: userID,
		})
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Initial lookup: last_used_at is nil.
	pre, err := r.FindByAKID(ctx, "AKIA-TOUCH-1")
	if err != nil {
		t.Fatalf("pre: %v", err)
	}
	if pre.LastUsedAt != nil {
		t.Fatalf("pre last_used_at should be nil, got %v", pre.LastUsedAt)
	}
	if err := r.TouchLastUsed(ctx, "AKIA-TOUCH-1"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	post, err := r.FindByAKID(ctx, "AKIA-TOUCH-1")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if post.LastUsedAt == nil {
		t.Fatalf("post last_used_at should be set")
	}
	// Touching a missing AKID is a silent no-op, not an error.
	if err := r.TouchLastUsed(ctx, "AKIA-DOES-NOT-EXIST"); err != nil {
		t.Fatalf("touch-missing: %v", err)
	}
}

func TestS3KeysInsertUniqueViolation(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-DUP", SecretEnc: []byte("x"),
			Label: "l", CreatedByUserID: userID,
		})
		return err
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-DUP", SecretEnc: []byte("x"),
			Label: "l2", CreatedByUserID: userID,
		})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("want UNIQUE, got %v", err)
	}
}
