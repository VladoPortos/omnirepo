package metadata_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// seedS3KeyID inserts an s3_access_keys row tied to the project that owns
// bucketID. Returns the freshly-minted s3_access_keys.id. Used by the actor-
// attribution tests so they have a real FK target for initiated_by_s3_key_id.
func seedS3KeyID(t *testing.T, db *metadata.DB, userID int64) int64 {
	t.Helper()
	ctx := context.Background()
	// Resolve the project id of the seeded bucket via the existing fixture
	// row in seedS3BucketAndUser ('mp-proj').
	var projectID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE name='mp-proj'`).Scan(&projectID); err != nil {
		t.Fatalf("resolve fixture project id: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO s3_access_keys(project_id, access_key_id, secret_enc, label, created_by_user_id)
		VALUES (?, ?, ?, ?, ?)
	`, projectID, "AKIA-actor-test-01", []byte("enc"), "actor-test", userID); err != nil {
		t.Fatalf("seed s3_access_keys: %v", err)
	}
	var keyID int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_access_keys WHERE access_key_id='AKIA-actor-test-01'`).Scan(&keyID); err != nil {
		t.Fatalf("resolve s3_access_keys id: %v", err)
	}
	return keyID
}

// TestStartUpload_AcceptsS3KeyID — the post-036 attribution path: an upload
// initiated via SigV4 carries an s3_access_keys.id pointer and leaves
// initiated_by_user_id NULL. StartUpload must accept this row, persist it,
// and FindUpload must read both pointer fields back accurately.
func TestStartUpload_AcceptsS3KeyID(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	s3KeyID := seedS3KeyID(t, db, userID)
	r := metadata.NewS3MultipartRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID:           "uuid-s3key",
			BucketID:           bucketID,
			Key:                "k/from-sigv4",
			InitiatedByS3KeyID: &s3KeyID,
		})
		return err
	}); err != nil {
		t.Fatalf("StartUpload (s3-key attributed): %v", err)
	}

	got, err := r.FindUpload(ctx, "uuid-s3key")
	if err != nil {
		t.Fatalf("FindUpload: %v", err)
	}
	if got.InitiatedByUserID != nil {
		t.Errorf("InitiatedByUserID = %v, want nil", *got.InitiatedByUserID)
	}
	if got.InitiatedByS3KeyID == nil || *got.InitiatedByS3KeyID != s3KeyID {
		t.Errorf("InitiatedByS3KeyID = %v, want &%d", got.InitiatedByS3KeyID, s3KeyID)
	}
}

// TestStartUpload_AcceptsUserID — the legacy / REST-API attribution path: an
// upload initiated through the user-authenticated REST surface (or the
// transitional fallback wrapper at multipart.go:91-95) carries a
// users.id pointer and leaves initiated_by_s3_key_id NULL. StartUpload must
// continue to accept this row.
func TestStartUpload_AcceptsUserID(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID:          "uuid-userid",
			BucketID:          bucketID,
			Key:               "k/from-rest",
			InitiatedByUserID: &userID,
		})
		return err
	}); err != nil {
		t.Fatalf("StartUpload (user-attributed): %v", err)
	}

	got, err := r.FindUpload(ctx, "uuid-userid")
	if err != nil {
		t.Fatalf("FindUpload: %v", err)
	}
	if got.InitiatedByUserID == nil || *got.InitiatedByUserID != userID {
		t.Errorf("InitiatedByUserID = %v, want &%d", got.InitiatedByUserID, userID)
	}
	if got.InitiatedByS3KeyID != nil {
		t.Errorf("InitiatedByS3KeyID = %v, want nil", *got.InitiatedByS3KeyID)
	}
}

// TestStartUpload_RequiresAttribution — both pointers nil must be rejected
// at the validation gate. Without this, an unauthenticated path could write
// an unattributed multipart row, defeating audit.
func TestStartUpload_RequiresAttribution(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, _ := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)

	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "uuid-orphan",
			BucketID: bucketID,
			Key:      "k/orphan",
			// Neither InitiatedByUserID nor InitiatedByS3KeyID set.
		})
		return e
	})
	if err == nil {
		t.Fatal("StartUpload accepted a row with neither attribution pointer set")
	}
}

// TestFindUpload_ScansNullableColumns — round-trip through FindUpload after
// inserting an S3-key-attributed row. The Scan path must read NULL for
// initiated_by_user_id and a populated *int64 for initiated_by_s3_key_id.
// Mirror of TestStartUpload_AcceptsS3KeyID but specifically asserting the
// Scan layer's nullable handling.
func TestFindUpload_ScansNullableColumns(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	s3KeyID := seedS3KeyID(t, db, userID)
	r := metadata.NewS3MultipartRepo(db)

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID:           "uuid-find-null",
			BucketID:           bucketID,
			Key:                "k/null-user",
			InitiatedByS3KeyID: &s3KeyID,
		})
		return err
	})

	got, err := r.FindUpload(ctx, "uuid-find-null")
	if err != nil {
		t.Fatalf("FindUpload: %v", err)
	}
	if got.InitiatedByUserID != nil {
		t.Errorf("FindUpload returned non-nil user id (%d) for s3-key-only row", *got.InitiatedByUserID)
	}
	if got.InitiatedByS3KeyID == nil {
		t.Fatal("FindUpload returned nil s3_key_id for s3-key-attributed row")
	}
	if *got.InitiatedByS3KeyID != s3KeyID {
		t.Errorf("FindUpload s3_key_id = %d, want %d", *got.InitiatedByS3KeyID, s3KeyID)
	}
}

// TestListStaleUploads_ScansNullableColumns — same round-trip property as
// TestFindUpload_ScansNullableColumns but exercising the ListStaleUploads
// SCAN path used by the boot-recovery sweeper.
func TestListStaleUploads_ScansNullableColumns(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	s3KeyID := seedS3KeyID(t, db, userID)
	r := metadata.NewS3MultipartRepo(db)

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID:           "uuid-stale-null",
			BucketID:           bucketID,
			Key:                "k/stale-user",
			InitiatedByS3KeyID: &s3KeyID,
		})
		return err
	})

	// Cutoff far in the future → the row is "stale" relative to it.
	stale, err := r.ListStaleUploads(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListStaleUploads: %v", err)
	}
	if len(stale) == 0 {
		t.Fatal("ListStaleUploads returned 0 rows; expected the seeded one")
	}
	var found *metadata.S3MultipartUpload
	for i := range stale {
		if stale[i].UploadID == "uuid-stale-null" {
			found = &stale[i]
			break
		}
	}
	if found == nil {
		t.Fatal("seeded uuid-stale-null not present in ListStaleUploads result")
	}
	if found.InitiatedByUserID != nil {
		t.Errorf("ListStaleUploads InitiatedByUserID = %v, want nil", *found.InitiatedByUserID)
	}
	if found.InitiatedByS3KeyID == nil || *found.InitiatedByS3KeyID != s3KeyID {
		t.Errorf("ListStaleUploads InitiatedByS3KeyID = %v, want &%d", found.InitiatedByS3KeyID, s3KeyID)
	}
}
