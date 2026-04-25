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

func TestS3KeysListByCreatedByUser_CrossProject_ExcludesRevoked(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	// Two projects, one user as creator, one extra project with a different
	// creator. The listing must return only keys created by the target
	// user, across both of their projects, excluding revoked ones.
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('mix1'),('mix2'),('other')`); err != nil {
		t.Fatalf("projects: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO users(login,email,password_hash) VALUES ('me','me@e.c','x'),('them','them@e.c','x')`); err != nil {
		t.Fatalf("users: %v", err)
	}
	var mix1, mix2, other, me, them int64
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='mix1'`).Scan(&mix1); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='mix2'`).Scan(&mix2); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='other'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM users WHERE login='me'`).Scan(&me); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM users WHERE login='them'`).Scan(&them); err != nil {
		t.Fatal(err)
	}

	r := metadata.NewS3KeysRepo(db)
	var revokedID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: mix1, AccessKeyID: "AKIA-ME-1", SecretEnc: []byte("x"),
			Label: "m1", CreatedByUserID: me,
		}); err != nil {
			return err
		}
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: mix2, AccessKeyID: "AKIA-ME-2", SecretEnc: []byte("x"),
			Label: "m2", CreatedByUserID: me,
		}); err != nil {
			return err
		}
		id, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: mix1, AccessKeyID: "AKIA-ME-REVOKED", SecretEnc: []byte("x"),
			Label: "revoked", CreatedByUserID: me,
		})
		revokedID = id
		if err != nil {
			return err
		}
		_, err = r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: other, AccessKeyID: "AKIA-THEM-1", SecretEnc: []byte("x"),
			Label: "theirs", CreatedByUserID: them,
		})
		return err
	}); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Revoke(ctx, tx, revokedID)
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	mine, err := r.ListByCreatedByUser(ctx, me)
	if err != nil {
		t.Fatalf("ListByCreatedByUser(me): %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("want 2 live keys, got %d: %+v", len(mine), mine)
	}
	for _, k := range mine {
		if k.CreatedByUserID != me {
			t.Fatalf("leaked key created by %d: %+v", k.CreatedByUserID, k)
		}
		if strings.Contains(k.AccessKeyID, "REVOKED") {
			t.Fatalf("revoked key leaked: %+v", k)
		}
	}

	theirs, err := r.ListByCreatedByUser(ctx, them)
	if err != nil {
		t.Fatalf("ListByCreatedByUser(them): %v", err)
	}
	if len(theirs) != 1 || theirs[0].AccessKeyID != "AKIA-THEM-1" {
		t.Fatalf("want exactly AKIA-THEM-1, got %+v", theirs)
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

// TestS3KeysRepo_RevokeAllForProject pins LIFECYCLE-01 cascade behavior.
// Given two live keys + one already-revoked key for the same project, the
// helper must stamp exactly the live keys with the supplied cascade
// timestamp and leave the pre-revoked key untouched.
func TestS3KeysRepo_RevokeAllForProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	// Insert three keys; revoke the third with a sentinel timestamp.
	var preRevokedID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-CASC-1", SecretEnc: []byte("x"),
			Label: "live1", CreatedByUserID: userID,
		}); err != nil {
			return err
		}
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-CASC-2", SecretEnc: []byte("x"),
			Label: "live2", CreatedByUserID: userID,
		}); err != nil {
			return err
		}
		v, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-CASC-PRE", SecretEnc: []byte("x"),
			Label: "pre", CreatedByUserID: userID,
		})
		preRevokedID = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE s3_access_keys SET revoked_at=? WHERE id=?`, preTS, preRevokedID,
	); err != nil {
		t.Fatalf("pre-revoke: %v", err)
	}

	// Cascade revoke at a chosen TS.
	const cascadeTS = "2026-04-25 12:34:56"
	var n int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var cErr error
		n, cErr = r.RevokeAllForProject(ctx, tx, projectID, cascadeTS)
		return cErr
	}); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if n != 2 {
		t.Fatalf("RevokeAllForProject: rows=%d, want 2", n)
	}

	// Both live keys carry cascadeTS; pre-revoked key keeps preTS.
	for _, akid := range []string{"AKIA-CASC-1", "AKIA-CASC-2"} {
		var got sql.NullString
		if err := db.Reader.QueryRowContext(ctx,
			`SELECT revoked_at FROM s3_access_keys WHERE access_key_id=?`, akid,
		).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", akid, err)
		}
		if !got.Valid || got.String != cascadeTS {
			t.Fatalf("%s revoked_at=%q want %q", akid, got.String, cascadeTS)
		}
	}
	var preGot sql.NullString
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT revoked_at FROM s3_access_keys WHERE id=?`, preRevokedID,
	).Scan(&preGot); err != nil {
		t.Fatalf("read pre: %v", err)
	}
	if !preGot.Valid || preGot.String != preTS {
		t.Fatalf("pre-revoked revoked_at=%q want %q (untouched)", preGot.String, preTS)
	}

	// Idempotency — second cascade with a different TS yields 0 rows.
	var n2 int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var cErr error
		n2, cErr = r.RevokeAllForProject(ctx, tx, projectID, "2099-12-31 23:59:59")
		return cErr
	}); err != nil {
		t.Fatalf("idempotent cascade: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second cascade rows=%d want 0", n2)
	}
	// Cascaded keys must still hold cascadeTS, not the new TS.
	for _, akid := range []string{"AKIA-CASC-1", "AKIA-CASC-2"} {
		var got sql.NullString
		_ = db.Reader.QueryRowContext(ctx,
			`SELECT revoked_at FROM s3_access_keys WHERE access_key_id=?`, akid,
		).Scan(&got)
		if got.String != cascadeTS {
			t.Fatalf("%s re-stamped to %q (not idempotent)", akid, got.String)
		}
	}
}

// TestS3KeysRepo_RestoreCascadedForProject pins LIFECYCLE-01 reverse
// cascade with timestamp-equality filter. Independently revoked keys must
// stay revoked after Restore.
func TestS3KeysRepo_RestoreCascadedForProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	var preRevokedID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-RES-1", SecretEnc: []byte("x"),
			Label: "live1", CreatedByUserID: userID,
		}); err != nil {
			return err
		}
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-RES-2", SecretEnc: []byte("x"),
			Label: "live2", CreatedByUserID: userID,
		}); err != nil {
			return err
		}
		v, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-RES-PRE", SecretEnc: []byte("x"),
			Label: "pre", CreatedByUserID: userID,
		})
		preRevokedID = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE s3_access_keys SET revoked_at=? WHERE id=?`, preTS, preRevokedID,
	); err != nil {
		t.Fatalf("pre-revoke: %v", err)
	}

	const cascadeTS = "2026-04-25 12:34:56"
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.RevokeAllForProject(ctx, tx, projectID, cascadeTS)
		return err
	}); err != nil {
		t.Fatalf("cascade: %v", err)
	}

	// Reverse cascade with the cascadeTS marker.
	var restored int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var rErr error
		restored, rErr = r.RestoreCascadedForProject(ctx, tx, projectID, cascadeTS)
		return rErr
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored != 2 {
		t.Fatalf("RestoreCascadedForProject: rows=%d want 2", restored)
	}
	for _, akid := range []string{"AKIA-RES-1", "AKIA-RES-2"} {
		var got sql.NullString
		_ = db.Reader.QueryRowContext(ctx,
			`SELECT revoked_at FROM s3_access_keys WHERE access_key_id=?`, akid,
		).Scan(&got)
		if got.Valid {
			t.Fatalf("%s revoked_at=%q want NULL after restore", akid, got.String)
		}
	}
	var preGot sql.NullString
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT revoked_at FROM s3_access_keys WHERE id=?`, preRevokedID,
	).Scan(&preGot)
	if !preGot.Valid || preGot.String != preTS {
		t.Fatalf("pre-revoked revoked_at=%q want %q (independent revoke must survive)", preGot.String, preTS)
	}

	// Restoring with a non-matching TS is a no-op.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		n, rErr := r.RestoreCascadedForProject(ctx, tx, projectID, "9999-99-99 99:99:99")
		if rErr != nil {
			return rErr
		}
		if n != 0 {
			t.Fatalf("non-match restore rows=%d want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("non-match: %v", err)
	}
}

// TestS3KeysRepo_FindByAKID_DeletedProject pins LIFECYCLE-05: a key whose
// owning project is soft-deleted MUST collapse to ErrS3AccessKeyNotFound.
//
// We soft-delete the project via a raw UPDATE rather than ProjectsRepo.SoftDelete
// so this test isolates the FindByAKID JOIN filter from the cascade in plan 01-01.
// (Plan 01-01's cascade would also revoke the key — masking the lookup-hardening
// behavior we're testing here.)
func TestS3KeysRepo_FindByAKID_DeletedProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	// Insert a live key.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-DEL-PROJ", SecretEnc: []byte("x"),
			Label: "live", CreatedByUserID: userID,
		})
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Sanity: live project + live key resolves.
	if _, err := r.FindByAKID(ctx, "AKIA-DEL-PROJ"); err != nil {
		t.Fatalf("pre-soft-delete: want resolve, got %v", err)
	}

	// Soft-delete the project via raw UPDATE (decoupled from plan 01-01 cascade).
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE projects SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, projectID,
	); err != nil {
		t.Fatalf("raw soft-delete: %v", err)
	}

	// FindByAKID must now collapse to ErrS3AccessKeyNotFound.
	_, err := r.FindByAKID(ctx, "AKIA-DEL-PROJ")
	if !errors.Is(err, metadata.ErrS3AccessKeyNotFound) {
		t.Fatalf("post-soft-delete: want ErrS3AccessKeyNotFound, got %v", err)
	}
}

// TestS3KeysRepo_FindByAKID_LiveProjectStillWorks pins regression check —
// the new JOIN must not break the happy path.
func TestS3KeysRepo_FindByAKID_LiveProjectStillWorks(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-LIVE-PROJ", SecretEnc: []byte("sealed"),
			Label: "happy", CreatedByUserID: userID,
		})
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := r.FindByAKID(ctx, "AKIA-LIVE-PROJ")
	if err != nil {
		t.Fatalf("FindByAKID: %v", err)
	}
	if got.ID != id || got.Label != "happy" || string(got.SecretEnc) != "sealed" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestS3KeysRepo_FindByAKID_NoOraclePreserved pins D-12: all three failure
// modes (missing key, revoked key in live project, live key in deleted
// project) MUST return the same error sentinel — no leak distinguishing
// "wrong project state" from "wrong key".
func TestS3KeysRepo_FindByAKID_NoOraclePreserved(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	// Setup: one live key (project A live), one revoked key (project A live),
	// one live key on a soft-deleted project (project B).
	var revokedID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-NO-ORACLE-LIVE", SecretEnc: []byte("x"),
			Label: "live", CreatedByUserID: userID,
		}); err != nil {
			return err
		}
		v, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-NO-ORACLE-REV", SecretEnc: []byte("x"),
			Label: "rev", CreatedByUserID: userID,
		})
		revokedID = v
		return err
	}); err != nil {
		t.Fatalf("insert batch 1: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Revoke(ctx, tx, revokedID)
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Project B with a live key, then soft-delete the project.
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('s3proj-dead')`); err != nil {
		t.Fatalf("seed project B: %v", err)
	}
	var deadProjectID int64
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='s3proj-dead'`).Scan(&deadProjectID); err != nil {
		t.Fatalf("find project B: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: deadProjectID, AccessKeyID: "AKIA-NO-ORACLE-DEADPROJ", SecretEnc: []byte("x"),
			Label: "deadproj", CreatedByUserID: userID,
		})
		return err
	}); err != nil {
		t.Fatalf("insert dead-project key: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE projects SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, deadProjectID,
	); err != nil {
		t.Fatalf("soft-delete project B: %v", err)
	}

	// (a) Live key in live project resolves.
	if _, err := r.FindByAKID(ctx, "AKIA-NO-ORACLE-LIVE"); err != nil {
		t.Fatalf("(a) live key: %v", err)
	}
	// (b) Revoked key in live project → ErrS3AccessKeyNotFound.
	_, errRev := r.FindByAKID(ctx, "AKIA-NO-ORACLE-REV")
	if !errors.Is(errRev, metadata.ErrS3AccessKeyNotFound) {
		t.Fatalf("(b) revoked key: want ErrS3AccessKeyNotFound, got %v", errRev)
	}
	// (c) Live key in soft-deleted project → ErrS3AccessKeyNotFound (SAME sentinel).
	_, errDead := r.FindByAKID(ctx, "AKIA-NO-ORACLE-DEADPROJ")
	if !errors.Is(errDead, metadata.ErrS3AccessKeyNotFound) {
		t.Fatalf("(c) deleted-project key: want ErrS3AccessKeyNotFound, got %v", errDead)
	}
	// (d) Missing key (sanity baseline).
	_, errMissing := r.FindByAKID(ctx, "AKIA-NEVER-EXISTED")
	if !errors.Is(errMissing, metadata.ErrS3AccessKeyNotFound) {
		t.Fatalf("(d) missing key: want ErrS3AccessKeyNotFound, got %v", errMissing)
	}

	// All three failure-mode errors are identical (no extra wrapping).
	if errRev.Error() != errMissing.Error() {
		t.Fatalf("oracle leak: revoked %q vs missing %q", errRev, errMissing)
	}
	if errDead.Error() != errMissing.Error() {
		t.Fatalf("oracle leak: deleted-project %q vs missing %q", errDead, errMissing)
	}
}

// TestS3KeysRepo_FindByAKID_RestoreReactivates pins reverse-cascade
// transparency: clearing projects.deleted_at must let FindByAKID resolve
// again (the JOIN filter is re-evaluated, not cached).
func TestS3KeysRepo_FindByAKID_RestoreReactivates(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projectID, userID := seedS3Project(t, db)
	r := metadata.NewS3KeysRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID: projectID, AccessKeyID: "AKIA-RESTORE-RT", SecretEnc: []byte("x"),
			Label: "rt", CreatedByUserID: userID,
		})
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Soft-delete project → not found.
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE projects SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, projectID,
	); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	if _, err := r.FindByAKID(ctx, "AKIA-RESTORE-RT"); !errors.Is(err, metadata.ErrS3AccessKeyNotFound) {
		t.Fatalf("post-delete: want ErrS3AccessKeyNotFound, got %v", err)
	}
	// Restore → found again.
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE projects SET deleted_at = NULL WHERE id = ?`, projectID,
	); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := r.FindByAKID(ctx, "AKIA-RESTORE-RT"); err != nil {
		t.Fatalf("post-restore: want resolve, got %v", err)
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
