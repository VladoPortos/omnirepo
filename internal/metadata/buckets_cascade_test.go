package metadata_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// TestS3Buckets_CascadeSoftDeleteForProject pins LIFECYCLE-02 cascade
// behavior. Two live buckets + one already-soft-deleted bucket → cascade
// stamps the live ones with the supplied timestamp; pre-deleted bucket
// stays as-is.
func TestS3Buckets_CascadeSoftDeleteForProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	// Seed project.
	var pid int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('bcasc')`)
		if err != nil {
			return err
		}
		pid, _ = res.LastInsertId()
		return nil
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Seed three buckets; soft-delete the third with a sentinel TS.
	var b1, b2, bPre int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO s3_buckets(name, project_id) VALUES ('b-live-1', ?)`, pid)
		if err != nil {
			return err
		}
		b1, _ = r.LastInsertId()
		r, err = tx.ExecContext(ctx, `INSERT INTO s3_buckets(name, project_id) VALUES ('b-live-2', ?)`, pid)
		if err != nil {
			return err
		}
		b2, _ = r.LastInsertId()
		r, err = tx.ExecContext(ctx, `INSERT INTO s3_buckets(name, project_id) VALUES ('b-pre', ?)`, pid)
		if err != nil {
			return err
		}
		bPre, _ = r.LastInsertId()
		return nil
	}); err != nil {
		t.Fatalf("seed buckets: %v", err)
	}
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE s3_buckets SET deleted_at=? WHERE id=?`, preTS, bPre,
	); err != nil {
		t.Fatalf("pre-delete: %v", err)
	}

	// Cascade.
	const cascadeTS = "2026-04-25 12:34:56"
	var n int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var cErr error
		n, cErr = metadata.SoftDeleteAllBucketsForProject(ctx, tx, pid, cascadeTS)
		return cErr
	}); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if n != 2 {
		t.Fatalf("SoftDeleteAllBucketsForProject rows=%d want 2", n)
	}
	// s3_buckets.deleted_at is TIMESTAMP affinity → modernc/sqlite normalizes
	// the read-back value to ISO-8601 ("YYYY-MM-DDTHH:MM:SSZ") even though we
	// wrote a "YYYY-MM-DD HH:MM:SS" string. WHERE-clause equality (the actual
	// path the cascade Restore uses) compares the bound parameter against the
	// stored bytes and DOES match — so we probe via WHERE-equality, not by
	// reading the value back as a string.
	for _, id := range []int64{b1, b2} {
		var match int
		if err := db.Reader.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM s3_buckets WHERE id=? AND deleted_at = ?`, id, cascadeTS,
		).Scan(&match); err != nil {
			t.Fatalf("equality probe bucket %d: %v", id, err)
		}
		if match != 1 {
			t.Fatalf("bucket %d: cascade timestamp not stored (equality probe got %d)", id, match)
		}
	}
	var preMatch int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM s3_buckets WHERE id=? AND deleted_at = ?`, bPre, preTS,
	).Scan(&preMatch)
	if preMatch != 1 {
		t.Fatalf("pre-deleted bucket: pre-TS clobbered (equality probe got %d)", preMatch)
	}

	// Idempotent.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		n2, e := metadata.SoftDeleteAllBucketsForProject(ctx, tx, pid, "2099-12-31 23:59:59")
		if e != nil {
			return e
		}
		if n2 != 0 {
			t.Fatalf("idempotent cascade rows=%d want 0", n2)
		}
		return nil
	}); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
}

// TestS3Buckets_RestoreCascadedForProject pins LIFECYCLE-02 reverse-cascade
// timestamp-equality semantics.
func TestS3Buckets_RestoreCascadedForProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	var pid int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('bres')`)
		if err != nil {
			return err
		}
		pid, _ = res.LastInsertId()
		return nil
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	var b1, b2, bPre int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO s3_buckets(name, project_id) VALUES ('br-1', ?)`, pid)
		if err != nil {
			return err
		}
		b1, _ = r.LastInsertId()
		r, err = tx.ExecContext(ctx, `INSERT INTO s3_buckets(name, project_id) VALUES ('br-2', ?)`, pid)
		if err != nil {
			return err
		}
		b2, _ = r.LastInsertId()
		r, err = tx.ExecContext(ctx, `INSERT INTO s3_buckets(name, project_id) VALUES ('br-pre', ?)`, pid)
		if err != nil {
			return err
		}
		bPre, _ = r.LastInsertId()
		return nil
	}); err != nil {
		t.Fatalf("seed buckets: %v", err)
	}
	const preTS = "1999-01-01 00:00:00"
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE s3_buckets SET deleted_at=? WHERE id=?`, preTS, bPre,
	); err != nil {
		t.Fatalf("pre-delete: %v", err)
	}

	const cascadeTS = "2026-04-25 12:34:56"
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := metadata.SoftDeleteAllBucketsForProject(ctx, tx, pid, cascadeTS)
		return err
	}); err != nil {
		t.Fatalf("cascade: %v", err)
	}

	var restored int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var rErr error
		restored, rErr = metadata.RestoreCascadedBucketsForProject(ctx, tx, pid, cascadeTS)
		return rErr
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored != 2 {
		t.Fatalf("restored=%d want 2", restored)
	}
	for _, id := range []int64{b1, b2} {
		var got sql.NullString
		_ = db.Reader.QueryRowContext(ctx, `SELECT deleted_at FROM s3_buckets WHERE id=?`, id).Scan(&got)
		if got.Valid {
			t.Fatalf("bucket %d deleted_at=%q want NULL", id, got.String)
		}
	}
	// Pre-deleted bucket must still match preTS via WHERE-equality.
	var preMatch int
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM s3_buckets WHERE id=? AND deleted_at = ?`, bPre, preTS,
	).Scan(&preMatch)
	if preMatch != 1 {
		t.Fatalf("pre-deleted bucket: independent delete must survive Restore (equality probe got %d)", preMatch)
	}

	// Non-matching TS is a no-op.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		n, rErr := metadata.RestoreCascadedBucketsForProject(ctx, tx, pid, "9999-99-99 99:99:99")
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
