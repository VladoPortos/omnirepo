package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func seedS3Bucket(t *testing.T, db *metadata.DB, name string) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES (?)`, name+"-proj"); err != nil {
		t.Fatalf("project: %v", err)
	}
	var projectID int64
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name=?`, name+"-proj").Scan(&projectID); err != nil {
		t.Fatalf("find project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO s3_buckets(name,project_id) VALUES (?,?)`, name, projectID); err != nil {
		t.Fatalf("bucket: %v", err)
	}
	var bucketID int64
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM s3_buckets WHERE name=?`, name).Scan(&bucketID); err != nil {
		t.Fatalf("find bucket: %v", err)
	}
	return bucketID
}

func TestS3ObjectsUpsertAndFind(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID := seedS3Bucket(t, db, "b1")
	r := metadata.NewS3ObjectsRepo(db)

	obj := &metadata.S3Object{
		BucketID:    bucketID,
		Key:         "docs/readme.txt",
		SizeBytes:   42,
		ETag:        `"deadbeef"`,
		ContentType: "text/plain",
		SHA256:      "sha256:abc",
	}
	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Upsert(ctx, tx, obj)
		id = v
		return err
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := r.FindByBucketAndKey(ctx, bucketID, "docs/readme.txt")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.ID != id || got.SizeBytes != 42 || got.ETag != `"deadbeef"` || got.ContentType != "text/plain" || got.MetadataJSON != "{}" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Upsert keeps id stable and replaces fields.
	obj.SizeBytes = 99
	obj.ETag = `"feed"`
	obj.SHA256 = "sha256:def"
	var id2 int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Upsert(ctx, tx, obj)
		id2 = v
		return err
	})
	if id2 != id {
		t.Fatalf("upsert changed id: %d -> %d", id, id2)
	}
	got2, _ := r.FindByBucketAndKey(ctx, bucketID, "docs/readme.txt")
	if got2.SizeBytes != 99 || got2.SHA256 != "sha256:def" {
		t.Fatalf("upsert fields not replaced: %+v", got2)
	}
}

func TestS3ObjectsFindMissing(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID := seedS3Bucket(t, db, "b2")
	r := metadata.NewS3ObjectsRepo(db)
	_, err := r.FindByBucketAndKey(ctx, bucketID, "nope")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestS3ObjectsDelete(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID := seedS3Bucket(t, db, "b3")
	r := metadata.NewS3ObjectsRepo(db)
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Upsert(ctx, tx, &metadata.S3Object{
			BucketID: bucketID, Key: "k", SizeBytes: 1,
			ETag: `"e"`, SHA256: "sha256:x",
		})
		return err
	})
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Delete(ctx, tx, bucketID, "k")
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.FindByBucketAndKey(ctx, bucketID, "k"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	// Idempotent.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Delete(ctx, tx, bucketID, "k")
	}); err != nil {
		t.Fatalf("delete idempotent: %v", err)
	}
}

func TestS3ObjectsListByBucket_PrefixPaginate(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	bucketID := seedS3Bucket(t, db, "b4")
	r := metadata.NewS3ObjectsRepo(db)

	// Seed 25 objects under "docs/" and 3 under "img/".
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 25; i++ {
			if _, err := r.Upsert(ctx, tx, &metadata.S3Object{
				BucketID: bucketID, Key: fmt.Sprintf("docs/file-%03d.txt", i),
				SizeBytes: int64(i), ETag: `"e"`, SHA256: "sha256:x",
			}); err != nil {
				return err
			}
		}
		for i := 0; i < 3; i++ {
			if _, err := r.Upsert(ctx, tx, &metadata.S3Object{
				BucketID: bucketID, Key: fmt.Sprintf("img/pic-%d.png", i),
				SizeBytes: int64(i), ETag: `"e"`, SHA256: "sha256:x",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First page: prefix docs/, maxKeys=10.
	page, err := r.ListByBucket(ctx, bucketID, "docs/", "", 10)
	if err != nil {
		t.Fatalf("list p1: %v", err)
	}
	if len(page.Objects) != 10 || !page.IsTruncated || page.NextToken == "" {
		t.Fatalf("p1 want 10 truncated with token, got %d truncated=%v token=%q", len(page.Objects), page.IsTruncated, page.NextToken)
	}

	// Second page.
	page2, err := r.ListByBucket(ctx, bucketID, "docs/", page.NextToken, 10)
	if err != nil {
		t.Fatalf("list p2: %v", err)
	}
	if len(page2.Objects) != 10 || !page2.IsTruncated {
		t.Fatalf("p2 want 10 truncated, got %d truncated=%v", len(page2.Objects), page2.IsTruncated)
	}

	// Third page: last 5 under docs/, not truncated.
	page3, err := r.ListByBucket(ctx, bucketID, "docs/", page2.NextToken, 10)
	if err != nil {
		t.Fatalf("list p3: %v", err)
	}
	if len(page3.Objects) != 5 || page3.IsTruncated {
		t.Fatalf("p3 want 5 not-truncated, got %d truncated=%v", len(page3.Objects), page3.IsTruncated)
	}
	// None of them should be from the img/ prefix.
	for _, o := range page3.Objects {
		if o.Key[:4] != "docs" {
			t.Fatalf("prefix leak: %s", o.Key)
		}
	}

	// img/ prefix alone.
	pageImg, err := r.ListByBucket(ctx, bucketID, "img/", "", 100)
	if err != nil {
		t.Fatalf("img list: %v", err)
	}
	if len(pageImg.Objects) != 3 || pageImg.IsTruncated {
		t.Fatalf("img list want 3 not-truncated, got %d truncated=%v", len(pageImg.Objects), pageImg.IsTruncated)
	}
}
