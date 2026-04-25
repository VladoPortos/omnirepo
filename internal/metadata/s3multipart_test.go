package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
			InitiatedByUserID: &userID,
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
			UploadID: "u2", BucketID: bucketID, Key: "k", InitiatedByUserID: &userID,
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
			UploadID: "u3", BucketID: bucketID, Key: "k", InitiatedByUserID: &userID,
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
			UploadID: "dup", BucketID: bucketID, Key: "k1", InitiatedByUserID: &userID,
		})
		return err
	})
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID: "dup", BucketID: bucketID, Key: "k2", InitiatedByUserID: &userID,
		})
		return err
	})
	if err == nil {
		t.Fatal("want UNIQUE error, got nil")
	}
}

// -- Plan 02-05 paginated repo helper tests (S3HARD-09 / S3HARD-10) --------

// seedUploads inserts uploads with the supplied (key, uploadID) pairs and
// returns the bucket id used. Callers pass them in lexicographic order if
// they want stable cursor tests.
func seedUploads(t *testing.T, db *metadata.DB, bucketID, userID int64, pairs [][2]string) {
	t.Helper()
	r := metadata.NewS3MultipartRepo(db)
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		for _, p := range pairs {
			if _, err := r.StartUpload(context.Background(), tx, &metadata.S3MultipartUpload{
				UploadID: p[1], BucketID: bucketID, Key: p[0], InitiatedByUserID: &userID,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seedUploads: %v", err)
	}
}

func TestListUploadsForBucketPaginated_TruncatesAtLimit(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	bucketID, userID := seedS3BucketAndUser(t, db)
	seedUploads(t, db, bucketID, userID, [][2]string{
		{"k1", "u1"}, {"k2", "u2"}, {"k3", "u3"},
	})
	r := metadata.NewS3MultipartRepo(db)
	rows, err := r.ListUploadsForBucketPaginated(context.Background(), bucketID, "", "", "", 2)
	if err != nil {
		t.Fatalf("paginated: %v", err)
	}
	// LIMIT ?+1 means caller asks for 2 → SQL returns 3 (so caller can detect truncation).
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (limit+1), got %d", len(rows))
	}
	if rows[0].Key != "k1" || rows[1].Key != "k2" || rows[2].Key != "k3" {
		t.Fatalf("unexpected order: %+v", rows)
	}
}

func TestListUploadsForBucketPaginated_AppliesDefault(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	bucketID, userID := seedS3BucketAndUser(t, db)
	seedUploads(t, db, bucketID, userID, [][2]string{{"k1", "u1"}, {"k2", "u2"}})
	r := metadata.NewS3MultipartRepo(db)
	// limit=0 must be clamped to 1000 → both rows returned with no error.
	rows, err := r.ListUploadsForBucketPaginated(context.Background(), bucketID, "", "", "", 0)
	if err != nil {
		t.Fatalf("paginated default: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows under default limit, got %d", len(rows))
	}
}

func TestListUploadsForBucketPaginated_RespectsCursor(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	bucketID, userID := seedS3BucketAndUser(t, db)
	pairs := [][2]string{
		{"k1", "u1"}, {"k2", "u2"}, {"k3", "u3"}, {"k4", "u4"}, {"k5", "u5"},
	}
	seedUploads(t, db, bucketID, userID, pairs)
	r := metadata.NewS3MultipartRepo(db)
	ctx := context.Background()

	// Page 1: limit=2 → returns 3 (limit+1); caller would use rows[1] as last-included.
	page1, err := r.ListUploadsForBucketPaginated(ctx, bucketID, "", "", "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 3 {
		t.Fatalf("page1: want 3 (limit+1), got %d", len(page1))
	}
	// Caller drops the extra row; last-included = page1[1].
	last := page1[1]

	// Page 2: cursor at page1[1]; limit=2 → SQL returns 3 (k3..k5 = 3 rows).
	page2, err := r.ListUploadsForBucketPaginated(ctx, bucketID, "", last.Key, last.UploadID, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 3 {
		t.Fatalf("page2: want 3 (limit+1), got %d (keys=%v)", len(page2), keysOf(page2))
	}
	last2 := page2[1]
	if last2.Key != "k4" {
		t.Fatalf("page2 last-included: want k4, got %s", last2.Key)
	}

	// Page 3: cursor at page2[1]; limit=2 → SQL returns 1 (k5 only — terminal page).
	page3, err := r.ListUploadsForBucketPaginated(ctx, bucketID, "", last2.Key, last2.UploadID, 2)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("page3: want 1 (terminal, no extra), got %d (keys=%v)", len(page3), keysOf(page3))
	}
	if page3[0].Key != "k5" {
		t.Fatalf("page3: want k5, got %s", page3[0].Key)
	}
}

func TestListUploadsForBucketPaginated_RespectsPrefix(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	bucketID, userID := seedS3BucketAndUser(t, db)
	seedUploads(t, db, bucketID, userID, [][2]string{
		{"a/k1", "u1"}, {"a/k2", "u2"}, {"b/k1", "u3"},
	})
	r := metadata.NewS3MultipartRepo(db)
	rows, err := r.ListUploadsForBucketPaginated(context.Background(), bucketID, "a/", "", "", 10)
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 a/-prefixed rows, got %d (%v)", len(rows), keysOf(rows))
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Key, "a/") {
			t.Fatalf("non-prefix row leaked: %s", r.Key)
		}
	}
}

func keysOf(rows []metadata.S3MultipartUpload) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Key
	}
	return out
}

func TestListPartsPaginated_TruncatesAtLimit(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := r.StartUpload(context.Background(), tx, &metadata.S3MultipartUpload{
			UploadID: "p-up", BucketID: bucketID, Key: "k", InitiatedByUserID: &userID,
		}); err != nil {
			return err
		}
		for i := 1; i <= 5; i++ {
			if err := r.AddPart(context.Background(), tx, &metadata.S3MultipartPart{
				UploadID: "p-up", PartNumber: i, SizeBytes: int64(i), MD5: "m",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed parts: %v", err)
	}
	parts, err := r.ListPartsPaginated(context.Background(), "p-up", 0, 3)
	if err != nil {
		t.Fatalf("paginated parts: %v", err)
	}
	if len(parts) != 4 {
		t.Fatalf("want 4 (limit+1), got %d", len(parts))
	}
	for i, p := range parts {
		if p.PartNumber != i+1 {
			t.Fatalf("parts not ordered: %+v", parts)
		}
	}
}

func TestListPartsPaginated_RespectsMarker(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	bucketID, userID := seedS3BucketAndUser(t, db)
	r := metadata.NewS3MultipartRepo(db)
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := r.StartUpload(context.Background(), tx, &metadata.S3MultipartUpload{
			UploadID: "p-mk", BucketID: bucketID, Key: "k", InitiatedByUserID: &userID,
		}); err != nil {
			return err
		}
		for i := 1; i <= 5; i++ {
			if err := r.AddPart(context.Background(), tx, &metadata.S3MultipartPart{
				UploadID: "p-mk", PartNumber: i, SizeBytes: int64(i), MD5: "m",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First call with marker=2, limit=3 → parts 3, 4, 5 → SQL returns 3 (no extra).
	parts, err := r.ListPartsPaginated(context.Background(), "p-mk", 2, 3)
	if err != nil {
		t.Fatalf("paginated: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("want 3, got %d", len(parts))
	}
	if parts[0].PartNumber != 3 || parts[2].PartNumber != 5 {
		t.Fatalf("unexpected parts: %+v", parts)
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
			UploadID: "stale-1", BucketID: bucketID, Key: "k", InitiatedByUserID: &userID,
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
