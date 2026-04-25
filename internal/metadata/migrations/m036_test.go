package migrations_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
)

type m036ColInfo struct {
	colType string
	notNull int
}

// readM036PragmaTableInfo reads PRAGMA table_info(<table>) into a map keyed by
// column name. Used by 036's tests to inspect schema shape across the
// up/down roundtrip without re-implementing the scan loop in every case.
func readM036PragmaTableInfo(t *testing.T, db *metadata.DB, table string) map[string]m036ColInfo {
	t.Helper()
	rows, err := db.Reader.QueryContext(context.Background(),
		`PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]m036ColInfo{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue, pk any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma row for %s: %v", table, err)
		}
		out[name] = m036ColInfo{colType: colType, notNull: notNull}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err for %s: %v", table, err)
	}
	return out
}

// TestMigration036_FreshApply verifies that on a fresh DB after applying every
// migration through 036:
//   - s3_multipart_uploads.initiated_by_user_id is NULLable (notnull=0)
//   - s3_multipart_uploads.initiated_by_s3_key_id exists, is INTEGER, NULLable
//   - the FK list still contains references to users, s3_buckets, AND
//     s3_access_keys (the new one).
//
// This is the schema shape that lets Plan 02-04 attribute multipart uploads
// to the resolved S3 access key (S3HARD-05) without fabricating a user.
func TestMigration036_FreshApply(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	cols := readM036PragmaTableInfo(t, db, "s3_multipart_uploads")

	userCol, ok := cols["initiated_by_user_id"]
	if !ok {
		t.Fatal("initiated_by_user_id column missing after migration 036")
	}
	if userCol.notNull != 0 {
		t.Errorf("initiated_by_user_id notnull = %d, want 0 (NULLable)", userCol.notNull)
	}
	if !strings.EqualFold(userCol.colType, "INTEGER") {
		t.Errorf("initiated_by_user_id type = %q, want INTEGER", userCol.colType)
	}

	keyCol, ok := cols["initiated_by_s3_key_id"]
	if !ok {
		t.Fatal("initiated_by_s3_key_id column missing after migration 036")
	}
	if keyCol.notNull != 0 {
		t.Errorf("initiated_by_s3_key_id notnull = %d, want 0 (NULLable)", keyCol.notNull)
	}
	if !strings.EqualFold(keyCol.colType, "INTEGER") {
		t.Errorf("initiated_by_s3_key_id type = %q, want INTEGER", keyCol.colType)
	}

	// Verify the FK references survive the rebuild via PRAGMA foreign_key_list.
	fkRows, err := db.Reader.QueryContext(ctx, `PRAGMA foreign_key_list(s3_multipart_uploads)`)
	if err != nil {
		t.Fatalf("PRAGMA foreign_key_list: %v", err)
	}
	defer func() { _ = fkRows.Close() }()
	var sawKeyFK, sawUserFK, sawBucketFK bool
	for fkRows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan fk row: %v", err)
		}
		switch from {
		case "initiated_by_s3_key_id":
			if table != "s3_access_keys" {
				t.Errorf("initiated_by_s3_key_id FK -> %q, want s3_access_keys", table)
			}
			sawKeyFK = true
		case "initiated_by_user_id":
			if table != "users" {
				t.Errorf("initiated_by_user_id FK -> %q, want users", table)
			}
			sawUserFK = true
		case "bucket_id":
			if table != "s3_buckets" {
				t.Errorf("bucket_id FK -> %q, want s3_buckets", table)
			}
			sawBucketFK = true
		}
	}
	if !sawKeyFK {
		t.Error("expected FK from initiated_by_s3_key_id -> s3_access_keys (missing)")
	}
	if !sawUserFK {
		t.Error("expected FK from initiated_by_user_id -> users (missing after rebuild)")
	}
	if !sawBucketFK {
		t.Error("expected FK from bucket_id -> s3_buckets (missing after rebuild)")
	}
}

// TestMigration036_DownUpRoundtrip verifies that applying 036.down.sql restores
// the original schema (initiated_by_user_id NOT NULL, no
// initiated_by_s3_key_id) and that re-applying 036.up.sql produces the same
// post-036 shape.
func TestMigration036_DownUpRoundtrip(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Apply 036.down.sql directly (runner has no revert path — D-11).
	downBody, err := fs.ReadFile(migrations.FS, "036_s3_multipart_actor_attribution.down.sql")
	if err != nil {
		t.Fatalf("read 036.down.sql: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, string(downBody)); err != nil {
		t.Fatalf("exec 036.down.sql: %v", err)
	}

	// Original shape: initiated_by_user_id NOT NULL, no initiated_by_s3_key_id.
	colsAfterDown := readM036PragmaTableInfo(t, db, "s3_multipart_uploads")
	user, ok := colsAfterDown["initiated_by_user_id"]
	if !ok {
		t.Fatal("after down: initiated_by_user_id missing")
	}
	if user.notNull != 1 {
		t.Errorf("after down: initiated_by_user_id notnull = %d, want 1", user.notNull)
	}
	if _, ok := colsAfterDown["initiated_by_s3_key_id"]; ok {
		t.Error("after down: initiated_by_s3_key_id should be gone")
	}

	// Re-apply 036.up.sql directly.
	upBody, err := fs.ReadFile(migrations.FS, "036_s3_multipart_actor_attribution.up.sql")
	if err != nil {
		t.Fatalf("read 036.up.sql: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, string(upBody)); err != nil {
		t.Fatalf("exec 036.up.sql (re-apply): %v", err)
	}

	// Post-up shape: matches TestMigration036_FreshApply.
	cols := readM036PragmaTableInfo(t, db, "s3_multipart_uploads")
	if user, ok := cols["initiated_by_user_id"]; !ok || user.notNull != 0 {
		t.Errorf("after re-up: initiated_by_user_id notnull = %d (ok=%v), want 0",
			user.notNull, ok)
	}
	if key, ok := cols["initiated_by_s3_key_id"]; !ok || key.notNull != 0 {
		t.Errorf("after re-up: initiated_by_s3_key_id notnull = %d (ok=%v), want 0",
			key.notNull, ok)
	}
}

// TestMigration036_PreservesData verifies that rows existing in
// s3_multipart_uploads before migration 036 survive the table rebuild with
// every column value intact.
func TestMigration036_PreservesData(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()

	// Apply everything, then exec 036.down.sql to bring schema back to pre-036
	// shape. From there we seed fixture rows and re-apply 036.up.sql to
	// verify the rebuild's INSERT-SELECT preserves them.
	applyReal(t, db)
	downBody, err := fs.ReadFile(migrations.FS, "036_s3_multipart_actor_attribution.down.sql")
	if err != nil {
		t.Fatalf("read down: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, string(downBody)); err != nil {
		t.Fatalf("exec down: %v", err)
	}

	// Seed user + project + bucket + multipart row.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(login,email,password_hash) VALUES ('alice','a@b.c','x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM users WHERE login='alice'`).Scan(&userID); err != nil {
		t.Fatalf("read user id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var projectID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE name='p1'`).Scan(&projectID); err != nil {
		t.Fatalf("read project id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name,project_id) VALUES ('b1',?)`, projectID); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	var bucketID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_buckets WHERE name='b1'`).Scan(&bucketID); err != nil {
		t.Fatalf("read bucket id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO s3_multipart_uploads(upload_id,bucket_id,key,initiated_by_user_id,metadata_json)
		VALUES ('preserve-uid', ?, 'k/preserve', ?, '{"author":"alice"}')
	`, bucketID, userID); err != nil {
		t.Fatalf("seed multipart row: %v", err)
	}

	// Re-apply 036.up.sql.
	upBody, err := fs.ReadFile(migrations.FS, "036_s3_multipart_actor_attribution.up.sql")
	if err != nil {
		t.Fatalf("read up: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, string(upBody)); err != nil {
		t.Fatalf("exec up: %v", err)
	}

	// Row must still be queryable, every column intact.
	var (
		gotUploadID, gotKey, gotMetaJSON string
		gotBucketID, gotUserID           int64
		gotS3KeyID                       *int64
	)
	if err := db.Reader.QueryRowContext(ctx, `
		SELECT upload_id, bucket_id, key, initiated_by_user_id, initiated_by_s3_key_id, metadata_json
		FROM s3_multipart_uploads WHERE upload_id='preserve-uid'
	`).Scan(&gotUploadID, &gotBucketID, &gotKey, &gotUserID, &gotS3KeyID, &gotMetaJSON); err != nil {
		t.Fatalf("read preserved row: %v", err)
	}
	if gotUploadID != "preserve-uid" || gotBucketID != bucketID || gotKey != "k/preserve" ||
		gotUserID != userID || gotMetaJSON != `{"author":"alice"}` {
		t.Errorf("row not preserved: uploadID=%q bucketID=%d key=%q userID=%d meta=%q",
			gotUploadID, gotBucketID, gotKey, gotUserID, gotMetaJSON)
	}
	if gotS3KeyID != nil {
		t.Errorf("preserved row should have NULL initiated_by_s3_key_id (back-fill not required), got %v", *gotS3KeyID)
	}
}

// TestMigration036_PartsFKSurvives verifies that after the s3_multipart_uploads
// table rebuild, the s3_multipart_parts FK on upload_id still cascades on
// parent delete. The rebuild preserves the upload_id column unchanged so the
// FK by-column-name reference remains valid.
func TestMigration036_PartsFKSurvives(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Seed parents.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(login,email,password_hash) VALUES ('u','u@e.c','x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	_ = db.Reader.QueryRow(`SELECT id FROM users WHERE login='u'`).Scan(&userID)
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES ('proj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var projectID int64
	_ = db.Reader.QueryRow(`SELECT id FROM projects WHERE name='proj'`).Scan(&projectID)
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name,project_id) VALUES ('b',?)`, projectID); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	var bucketID int64
	_ = db.Reader.QueryRow(`SELECT id FROM s3_buckets WHERE name='b'`).Scan(&bucketID)

	// Insert upload row (post-036 shape; user-id-only attribution still legal).
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO s3_multipart_uploads(upload_id,bucket_id,key,initiated_by_user_id)
		VALUES ('cascadetest', ?, 'k', ?)
	`, bucketID, userID); err != nil {
		t.Fatalf("insert upload: %v", err)
	}

	// Insert a parts row referencing the surviving upload_id.
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO s3_multipart_parts(upload_id, part_number, size_bytes, md5)
		VALUES ('cascadetest', 1, 1024, 'md5hex')
	`); err != nil {
		t.Fatalf("insert part: %v", err)
	}

	// DELETE the upload — parts should cascade-delete.
	if _, err := db.Writer.ExecContext(ctx,
		`DELETE FROM s3_multipart_uploads WHERE upload_id='cascadetest'`); err != nil {
		t.Fatalf("delete upload: %v", err)
	}
	var partsRemaining int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM s3_multipart_parts WHERE upload_id='cascadetest'`).Scan(&partsRemaining); err != nil {
		t.Fatalf("count parts: %v", err)
	}
	if partsRemaining != 0 {
		t.Errorf("parts FK did not cascade — %d parts remain after parent delete", partsRemaining)
	}
}
