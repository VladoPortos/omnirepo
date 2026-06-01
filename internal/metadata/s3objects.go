// Package metadata — S3ObjectsRepo owns the s3_objects table.
// One row per committed object under a bucket. Upserts use
// the per-bucket mutex in the handler layer to serialize writers, but the
// DB itself also enforces UNIQUE(bucket_id,key) via the migration.
package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// S3Object mirrors one s3_objects row.
type S3Object struct {
	ID           int64
	BucketID     int64
	Key          string
	SizeBytes    int64
	ETag         string
	ContentType  string
	MetadataJSON string // canonical JSON serialization; caller owns Marshal
	SHA256       string
	CreatedAt    time.Time
}

// S3ObjectsRepo is the typed repo for s3_objects.
type S3ObjectsRepo struct{ db *DB }

// NewS3ObjectsRepo constructs the repo bound to db.
func NewS3ObjectsRepo(db *DB) *S3ObjectsRepo { return &S3ObjectsRepo{db: db} }

// Upsert writes (or replaces) the (bucket_id,key) row. Returns the row id.
// Uses INSERT ... ON CONFLICT DO UPDATE so the bucket mutex does not have
// to branch on insert-vs-update.
func (r *S3ObjectsRepo) Upsert(ctx context.Context, tx *sql.Tx, row *S3Object) (int64, error) {
	if row == nil {
		return 0, errors.New("s3_objects: nil row")
	}
	if row.BucketID == 0 || row.Key == "" || row.ETag == "" || row.SHA256 == "" {
		return 0, errors.New("s3_objects: bucket_id, key, etag, sha256 required")
	}
	meta := row.MetadataJSON
	if meta == "" {
		meta = "{}"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO s3_objects(bucket_id, key, size_bytes, etag, content_type, metadata_json, sha256)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_id, key) DO UPDATE SET
			size_bytes    = excluded.size_bytes,
			etag          = excluded.etag,
			content_type  = excluded.content_type,
			metadata_json = excluded.metadata_json,
			sha256        = excluded.sha256,
			created_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, row.BucketID, row.Key, row.SizeBytes, row.ETag, row.ContentType, meta, row.SHA256); err != nil {
		return 0, fmt.Errorf("s3_objects: upsert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM s3_objects WHERE bucket_id=? AND key=?`, row.BucketID, row.Key,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("s3_objects: read-back: %w", err)
	}
	return id, nil
}

// FindByBucketAndKey returns the row for (bucketID,key). Returns ErrNotFound.
func (r *S3ObjectsRepo) FindByBucketAndKey(ctx context.Context, bucketID int64, key string) (*S3Object, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, bucket_id, key, size_bytes, etag, content_type, metadata_json, sha256, created_at
		FROM s3_objects WHERE bucket_id=? AND key=?
	`, bucketID, key)
	o, err := scanS3Object(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3_objects: find: %w", err)
	}
	return o, nil
}

// Delete removes the (bucket_id,key) row. Idempotent.
func (r *S3ObjectsRepo) Delete(ctx context.Context, tx *sql.Tx, bucketID int64, key string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM s3_objects WHERE bucket_id=? AND key=?`, bucketID, key,
	); err != nil {
		return fmt.Errorf("s3_objects: delete: %w", err)
	}
	return nil
}

// ListPage is one page of ListObjectsV2-style results.
type ListPage struct {
	Objects     []S3Object
	NextToken   string // continuation marker; empty when page is last
	IsTruncated bool
}

// ListByBucket returns a paginated slice of objects under bucketID whose
// keys share `prefix`. Uses key > marker for cursor pagination (stable
// under concurrent insert because the ORDER BY key is unique).
//
// Hard cap: maxKeys is clamped to [1, 1000] (AWS default). When more rows
// remain past the returned page, NextToken is set to the last key.
func (r *S3ObjectsRepo) ListByBucket(ctx context.Context, bucketID int64, prefix, marker string, maxKeys int) (ListPage, error) {
	if maxKeys <= 0 {
		maxKeys = 1000
	}
	if maxKeys > 1000 {
		maxKeys = 1000
	}
	// Fetch one extra row to detect truncation without a second COUNT query.
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, bucket_id, key, size_bytes, etag, content_type, metadata_json, sha256, created_at
		FROM s3_objects
		WHERE bucket_id = ? AND key > ? AND key LIKE ? || '%'
		ORDER BY key
		LIMIT ?
	`, bucketID, marker, prefix, maxKeys+1)
	if err != nil {
		return ListPage{}, fmt.Errorf("s3_objects: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []S3Object
	for rows.Next() {
		o, err := scanS3Object(rows)
		if err != nil {
			return ListPage{}, fmt.Errorf("s3_objects: scan: %w", err)
		}
		out = append(out, *o)
	}
	if err := rows.Err(); err != nil {
		return ListPage{}, fmt.Errorf("s3_objects: rows: %w", err)
	}
	page := ListPage{Objects: out}
	if len(out) > maxKeys {
		page.Objects = out[:maxKeys]
		page.IsTruncated = true
		page.NextToken = page.Objects[len(page.Objects)-1].Key
	}
	return page, nil
}

func scanS3Object(rs scanner) (*S3Object, error) {
	var o S3Object
	var created string
	if err := rs.Scan(
		&o.ID, &o.BucketID, &o.Key, &o.SizeBytes, &o.ETag,
		&o.ContentType, &o.MetadataJSON, &o.SHA256, &created,
	); err != nil {
		return nil, err
	}
	o.CreatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", created)
	return &o, nil
}
