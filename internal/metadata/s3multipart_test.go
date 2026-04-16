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

func seedS3BucketAndUser(t *testing.T, db *metadata.DB) (bucketID, userID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('mp-proj')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	var projectID int64
	_ = db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='mp-proj'`).Scan(&projectID)
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO s3_buckets(name,project_id) VALUES ('mp-b',?)`, projectID); err != nil {
		t.Fatalf("bucket: %v", err)
	}
	_ = db.Reader.QueryRowContext(ctx, `SELECT id FROM s3_buckets WHERE name='mp-b'`).Scan(&bucketID)
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO users(login,email,password_hash) VALUES ('mp-u','u@e.c','x')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	_ = db.Reader.QueryRowContext(ctx, `SELECT id FROM users WHERE login='mp-u'`).Scan(&userID)
	return bucketID, userID
}

func TestS3MultipartStartAndFind(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "uuid-1", BucketID: bucketID, Key: "big/file",
			InitiatedByUserID: userID,
			MetadataJSON:      `{"x-amz-meta-author":"alice"}`,
		})
		id = v
		return err
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if id == 0 {
		t.Fatal("StartUpload returned zero id")
	}

	got, err := r.FindUpload(ctx, "uuid-1")
	if err != nil {
		t.Fatalf("FindUpload: %v", err)
	}
	if got.BucketID != bucketID || got.Key != "big/file" || got.MetadataJSON != `{"x-amz-meta-author":"alice"}` {
		t.Fatalf("mismatch: %+v", got)
	}

	_, err = r.FindUpload(ctx, "uuid-nope")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("missing: want ErrNotFound, got %v", err)
	}
}

func TestS3MultipartAddPartAndList(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "u2", BucketID: bucketID, Key: "k", InitiatedByUserID: userID,
		}); err != nil {
			return err
		}
		for i, md5 := range []string{"m1", "m2", "m3"} {
			if err := r.AddPart(ctx, tx, &metadata.S3MultipartPart{
				UploadID: "u2", PartNumber: i + 1, SizeBytes: 1024 * int64(i+1), MD5: md5,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed parts: %v", err)
	}

	parts, err := r.ListParts(ctx, "u2")
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	if parts[0].PartNumber != 1 || parts[2].PartNumber != 3 {
		t.Fatalf("parts not ordered: %+v", parts)
	}

	// Re-uploading part 2 replaces md5/size.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.AddPart(ctx, tx, &metadata.S3MultipartPart{
			UploadID: "u2", PartNumber: 2, SizeBytes: 9999, MD5: "m2-new",
		})
	}); err != nil {
		t.Fatalf("re-upload part: %v", err)
	}
	parts2, _ := r.ListParts(ctx, "u2")
	if parts2[1].MD5 != "m2-new" || parts2[1].SizeBytes != 9999 {
		t.Fatalf("part2 not replaced: %+v", parts2[1])
	}
}

func TestS3MultipartDeleteCascades(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "u3", BucketID: bucketID, Key: "k", InitiatedByUserID: userID,
		}); err != nil {
			return err
		}
		return r.AddPart(ctx, tx, &metadata.S3MultipartPart{
			UploadID: "u3", PartNumber: 1, SizeBytes: 1, MD5: "m",
		})
	})

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.DeleteUpload(ctx, tx, "u3")
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	parts, err := r.ListParts(ctx, "u3")
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(parts) != 0 {
		t.Fatalf("parts not cascaded: %d remaining", len(parts))
	}
	if _, err := r.FindUpload(ctx, "u3"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("upload after delete: want ErrNotFound, got %v", err)
	}
}

func TestS3MultipartStartUniqueViolation(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "dup", BucketID: bucketID, Key: "k1", InitiatedByUserID: userID,
		})
		return err
	})
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "dup", BucketID: bucketID, Key: "k2", InitiatedByUserID: userID,
		})
		return err
	})
	if err == nil {
		t.Fatal("want UNIQUE error, got nil")
	}
}

func TestS3MultipartListStaleUploads(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "stale-1", BucketID: bucketID, Key: "k", InitiatedByUserID: userID,
		})
		return err
	})

	// Cutoff one hour in the future → the just-inserted row is "stale".
	future := time.Now().UTC().Add(time.Hour)
	stale, err := r.ListStaleUploads(ctx, future)
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	if len(stale) != 1 || stale[0].UploadID != "stale-1" {
		t.Fatalf("stale list mismatch: %+v", stale)
	}

	// Cutoff in the past → no stale rows.
	past := time.Now().UTC().Add(-24 * time.Hour)
	stale2, err := r.ListStaleUploads(ctx, past)
	if err != nil {
		t.Fatalf("stale past: %v", err)
	}
	if len(stale2) != 0 {
		t.Fatalf("want 0 stale with past cutoff, got %d", len(stale2))
	}
}
