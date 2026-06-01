// internal-package test that exercises the package-private cascadeStepHook
// injection point on ProjectsRepo. Lives in `package metadata` (not
// `metadata_test`) so it can call the unexported withCascadeStepHookForTest
// setter — the hook is deliberately not exported because it has no API
// stability guarantees and exists only to prove cascade atomicity.
package metadata

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vladoportos/omnirepo/internal/metadata/migrations"
)

// newAtomicityTestDB builds a fresh, isolated, fully-migrated *DB for one
// test. Mirrors sqlitetest.New but lives inside the metadata package so the
// internal test file can reach package-private types.
func newAtomicityTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := migrations.Apply(context.Background(), db.Writer); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestProjectsRepo_SoftDelete_Atomicity — proof of cascade atomicity.
// Inject a failure into the cascade chain via cascadeStepHook; assert that
// the entire WriteTx rolls back so no row in any of the four affected tables
// reflects the cascade. This is the load-bearing atomicity invariant: if the
// cascade can half-commit, every other guarantee collapses.
func TestProjectsRepo_SoftDelete_Atomicity(t *testing.T) {
	db := newAtomicityTestDB(t)
	ctx := context.Background()

	// Seed: project + 2 live S3 keys + 2 live buckets + 1 live project-owned
	// api_key + 1 live user-owned api_key (latter to prove user-owned spare).
	r := NewProjectsRepo(db)
	pid, err := r.Create(ctx, "atomicity", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Seed user (raw INSERT — keep this internal-test file dependency-light).
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(login, email, password_hash) VALUES ('atomic-user','u@x','h')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var uid int64
	_ = db.Reader.QueryRowContext(ctx, `SELECT id FROM users WHERE login='atomic-user'`).Scan(&uid)

	skeys := NewS3KeysRepo(db)
	for _, akid := range []string{"AKIA-ATOM-1", "AKIA-ATOM-2"} {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := skeys.Insert(ctx, tx, &S3AccessKey{
				ProjectID: pid, AccessKeyID: akid, SecretEnc: []byte("x"),
				Label: akid, CreatedByUserID: uid,
			})
			return err
		}); err != nil {
			t.Fatalf("insert s3 key %s: %v", akid, err)
		}
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES ('atom-1', ?), ('atom-2', ?)`, pid, pid,
	); err != nil {
		t.Fatalf("insert buckets: %v", err)
	}
	akeys := NewAPIKeysRepo(db)
	pkID, err := akeys.CreateProjectKey(ctx, pid, "ci", "atompk01", "shaAPK")
	if err != nil {
		t.Fatalf("CreateProjectKey: %v", err)
	}
	ukID, err := akeys.CreateUserKey(ctx, uid, "u-key", "atomuk01", "shaAUK")
	if err != nil {
		t.Fatalf("CreateUserKey: %v", err)
	}

	// Install a hook that fails at the buckets step (after s3_keys cascade has
	// already executed inside the same tx).
	r.withCascadeStepHookForTest(func(step string) error {
		if step == "buckets" {
			return errors.New("synthetic buckets-step failure")
		}
		return nil
	})

	// SoftDelete must surface the synthetic error.
	err = r.SoftDelete(ctx, pid)
	if err == nil {
		t.Fatal("SoftDelete: expected error from hook, got nil")
	}
	if !strings.Contains(err.Error(), "synthetic buckets-step failure") {
		t.Fatalf("SoftDelete: error %q does not wrap synthetic failure", err)
	}

	// Assert ENTIRE tx rolled back — no row reflects the cascade.

	// 1) Project row NOT soft-deleted.
	var pdel sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE id=?`, pid).Scan(&pdel)
	if pdel.Valid {
		t.Fatalf("project deleted_at=%q, want NULL (rollback)", pdel.String)
	}

	// 2) Both S3 keys still live.
	for _, akid := range []string{"AKIA-ATOM-1", "AKIA-ATOM-2"} {
		var rev sql.NullString
		_ = db.Reader.QueryRowContext(ctx,
			`SELECT revoked_at FROM s3_access_keys WHERE access_key_id=?`, akid,
		).Scan(&rev)
		if rev.Valid {
			t.Fatalf("s3 key %s revoked_at=%q, want NULL (rollback)", akid, rev.String)
		}
	}

	// 3) Both buckets still live (cascade never ran for buckets — hook failed
	//    BEFORE the buckets UPDATE in our SoftDelete order, but the rollback
	//    matters most for any rows the s3_keys step DID stamp).
	for _, bn := range []string{"atom-1", "atom-2"} {
		var bdel sql.NullString
		_ = db.Reader.QueryRowContext(ctx,
			`SELECT deleted_at FROM s3_buckets WHERE name=?`, bn,
		).Scan(&bdel)
		if bdel.Valid {
			t.Fatalf("bucket %s deleted_at=%q, want NULL (rollback)", bn, bdel.String)
		}
	}

	// 4) Project-owned api_key still live.
	var pkRev sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, pkID).Scan(&pkRev)
	if pkRev.Valid {
		t.Fatalf("project api key revoked_at=%q, want NULL (rollback)", pkRev.String)
	}

	// 5) User-owned api_key still live (would have been spared anyway).
	var ukRev sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE id=?`, ukID).Scan(&ukRev)
	if ukRev.Valid {
		t.Fatalf("user api key revoked_at=%q, want NULL", ukRev.String)
	}

	// Tear down hook so a subsequent SoftDelete (regression check) succeeds.
	r.withCascadeStepHookForTest(nil)
	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("post-rollback SoftDelete (no hook): %v", err)
	}
	// Confirm cascade now fires.
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE id=?`, pid).Scan(&pdel)
	if !pdel.Valid {
		t.Fatal("post-rollback SoftDelete: project deleted_at NULL, want stamp")
	}
}

// TestProjectsRepo_Restore_Atomicity — restoring is atomic too. Inject a
// failure mid-restore-cascade and assert the project stays soft-deleted and
// child rows stay revoked / soft-deleted.
func TestProjectsRepo_Restore_Atomicity(t *testing.T) {
	db := newAtomicityTestDB(t)
	ctx := context.Background()
	r := NewProjectsRepo(db)
	pid, err := r.Create(ctx, "atomicity-restore", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(login, email, password_hash) VALUES ('atom-r','u@x','h')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var uid int64
	_ = db.Reader.QueryRowContext(ctx, `SELECT id FROM users WHERE login='atom-r'`).Scan(&uid)
	skeys := NewS3KeysRepo(db)
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := skeys.Insert(ctx, tx, &S3AccessKey{
			ProjectID: pid, AccessKeyID: "AKIA-AR-1", SecretEnc: []byte("x"),
			Label: "live", CreatedByUserID: uid,
		})
		return err
	}); err != nil {
		t.Fatalf("insert s3 key: %v", err)
	}

	// Soft-delete first (no hook).
	if err := r.SoftDelete(ctx, pid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Install hook that fails on the restore-cascade buckets step.
	r.withCascadeStepHookForTest(func(step string) error {
		if step == "buckets" {
			return errors.New("synthetic restore buckets failure")
		}
		return nil
	})

	if err := r.Restore(ctx, pid); err == nil {
		t.Fatal("Restore: expected error from hook, got nil")
	}

	// Project must still be soft-deleted (rollback).
	var pdel sql.NullString
	_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE id=?`, pid).Scan(&pdel)
	if !pdel.Valid {
		t.Fatal("project deleted_at NULL post-failed-restore, want still tombstoned")
	}
	// S3 key must still be revoked.
	var rev sql.NullString
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT revoked_at FROM s3_access_keys WHERE access_key_id='AKIA-AR-1'`,
	).Scan(&rev)
	if !rev.Valid {
		t.Fatal("s3 key revoked_at NULL post-failed-restore, want still revoked")
	}
}
