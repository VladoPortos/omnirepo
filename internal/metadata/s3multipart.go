// Package metadata — S3MultipartRepo owns the s3_multipart_uploads +
// s3_multipart_parts pair. Rows are
// short-lived: a client's InitiateMultipartUpload → UploadPart(*N) →
// CompleteMultipartUpload sequence exists entirely inside this pair until
// Complete promotes the assembly into s3_objects and deletes every row for
// the upload. AbortMultipartUpload deletes the upload row; the ON DELETE
// CASCADE on s3_multipart_parts.upload_id drops the part rows.
//
// The daily cleanup job aborts uploads whose initiated_at is older
// than 24 hours to reclaim staging bytes.
package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// S3MultipartUpload mirrors one s3_multipart_uploads row.
//
// Attribution model (post-migration 036):
//
//   - InitiatedByUserID is set when the upload came in through the user-
//     authenticated REST surface (or through the fallback wrapper at
//     internal/protocol/s3/backend/multipart.go).
//   - InitiatedByS3KeyID is set when the upload came in through the SigV4-
//     authenticated S3 surface (the typical case).
//   - At least one of the two MUST be non-nil — StartUpload enforces that
//     at the validation gate. Both nil = unattributed = audit gap.
//
// Both pointers nil-able to model SQL NULL: scan via sql.NullInt64 then
// convert (mirrors the apikeys.go owner_user_id / owner_project_id pattern).
type S3MultipartUpload struct {
	ID                 int64
	UploadID           string // UUID v4, client-visible handle
	BucketID           int64
	Key                string
	InitiatedByUserID  *int64 // NULLable post-036 (was int64)
	InitiatedByS3KeyID *int64 // new in 036; nil for legacy/user-attributed rows
	InitiatedAt        time.Time
	MetadataJSON       string // canonical JSON serialization of user metadata headers
}

// S3MultipartPart mirrors one s3_multipart_parts row.
type S3MultipartPart struct {
	ID         int64
	UploadID   string
	PartNumber int
	SizeBytes  int64
	MD5        string
	UploadedAt time.Time
}

// S3MultipartRepo is the typed repo.
type S3MultipartRepo struct{ db *DB }

// NewS3MultipartRepo constructs the repo bound to db.
func NewS3MultipartRepo(db *DB) *S3MultipartRepo { return &S3MultipartRepo{db: db} }

// StartUpload inserts a new upload row. Returns the row id. `UploadID`
// uniqueness enforces idempotency: if the caller re-uses a UUID the DB
// surfaces a UNIQUE error.
//
// Attribution: at least one of InitiatedByUserID / InitiatedByS3KeyID must be
// non-nil. Both nil is rejected at the validation gate to prevent silent
// audit gaps. Both set is permitted but unusual — typically a
// row carries exactly one.
//
// modernc/sqlite handles `*int64` arguments to ExecContext as
// "NULL when nil, integer when non-nil" without needing sql.NullInt64
// wrapping (mirrors the apikeys.go INSERT pattern).
func (r *S3MultipartRepo) StartUpload(ctx context.Context, tx *sql.Tx, row *S3MultipartUpload) (int64, error) {
	if row == nil {
		return 0, errors.New("s3_multipart_uploads: nil row")
	}
	if row.UploadID == "" || row.BucketID == 0 || row.Key == "" {
		return 0, errors.New("s3_multipart_uploads: upload_id, bucket_id, key required")
	}
	if row.InitiatedByUserID == nil && row.InitiatedByS3KeyID == nil {
		return 0, errors.New("s3_multipart_uploads: either initiated_by_user_id or initiated_by_s3_key_id required")
	}
	meta := row.MetadataJSON
	if meta == "" {
		meta = "{}"
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO s3_multipart_uploads(upload_id, bucket_id, key, initiated_by_user_id, initiated_by_s3_key_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, row.UploadID, row.BucketID, row.Key, row.InitiatedByUserID, row.InitiatedByS3KeyID, meta)
	if err != nil {
		return 0, fmt.Errorf("s3_multipart_uploads: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("s3_multipart_uploads: last insert id: %w", err)
	}
	return id, nil
}

// AddPart writes (or replaces) one part row. Replacing is intentional:
// AWS S3 clients may re-upload the same part number to correct a network
// blip, and the new bytes must win.
func (r *S3MultipartRepo) AddPart(ctx context.Context, tx *sql.Tx, row *S3MultipartPart) error {
	if row == nil {
		return errors.New("s3_multipart_parts: nil row")
	}
	if row.UploadID == "" || row.PartNumber <= 0 || row.MD5 == "" {
		return errors.New("s3_multipart_parts: upload_id, part_number (>0), md5 required")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO s3_multipart_parts(upload_id, part_number, size_bytes, md5)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(upload_id, part_number) DO UPDATE SET
			size_bytes  = excluded.size_bytes,
			md5         = excluded.md5,
			uploaded_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, row.UploadID, row.PartNumber, row.SizeBytes, row.MD5); err != nil {
		return fmt.Errorf("s3_multipart_parts: upsert: %w", err)
	}
	return nil
}

// FindUpload returns the upload row by its client-visible UploadID.
// Returns ErrNotFound on miss.
//
// initiated_by_user_id and initiated_by_s3_key_id are both NULLable post-036.
// Scan via sql.NullInt64 then convert to *int64 (apikeys.go pattern).
func (r *S3MultipartRepo) FindUpload(ctx context.Context, uploadID string) (*S3MultipartUpload, error) {
	var u S3MultipartUpload
	var initiated string
	var userID, s3KeyID sql.NullInt64
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, upload_id, bucket_id, key, initiated_by_user_id, initiated_by_s3_key_id, initiated_at, metadata_json
		FROM s3_multipart_uploads WHERE upload_id = ?
	`, uploadID).Scan(
		&u.ID, &u.UploadID, &u.BucketID, &u.Key, &userID, &s3KeyID, &initiated, &u.MetadataJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3_multipart_uploads: find: %w", err)
	}
	if userID.Valid {
		v := userID.Int64
		u.InitiatedByUserID = &v
	}
	if s3KeyID.Valid {
		v := s3KeyID.Int64
		u.InitiatedByS3KeyID = &v
	}
	u.InitiatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", initiated)
	return &u, nil
}

// ListParts returns every part for uploadID ordered by part_number ASC.
func (r *S3MultipartRepo) ListParts(ctx context.Context, uploadID string) ([]S3MultipartPart, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, upload_id, part_number, size_bytes, md5, uploaded_at
		FROM s3_multipart_parts WHERE upload_id = ?
		ORDER BY part_number ASC
	`, uploadID)
	if err != nil {
		return nil, fmt.Errorf("s3_multipart_parts: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []S3MultipartPart
	for rows.Next() {
		var p S3MultipartPart
		var uploaded string
		if err := rows.Scan(&p.ID, &p.UploadID, &p.PartNumber, &p.SizeBytes, &p.MD5, &uploaded); err != nil {
			return nil, fmt.Errorf("s3_multipart_parts: scan: %w", err)
		}
		p.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListUploadsForBucketPaginated returns up to (limit+1) rows so the caller
// can compute IsTruncated. Rows are ordered by (key, upload_id) for stable
// cursor pagination across pages.
//
// Cursor semantics: pass markerKey="" + markerUploadID="" for the first page.
// On subsequent pages pass the (key, upload_id) of the LAST INCLUDED row from
// the previous page; this helper returns rows strictly AFTER that pair under
// the lexicographic (key, upload_id) order.
//
// Limit clamping: values <=0 or >1000 collapse to the AWS-spec default of
// 1000. The SQL applies LIMIT (?+1) so callers can detect truncation by
// comparing len(rows) > limit.
//
// Prefix: empty string disables the prefix filter. Non-empty prefix is
// applied as a `key LIKE prefix || '%'` predicate, which composes with the
// cursor predicate via AND.
//
// All inputs are SQL bind parameters — no string concatenation, no SQL
// injection surface.
func (r *S3MultipartRepo) ListUploadsForBucketPaginated(ctx context.Context, bucketID int64, prefix, markerKey, markerUploadID string, limit int) ([]S3MultipartUpload, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, upload_id, bucket_id, key, initiated_by_user_id, initiated_by_s3_key_id, initiated_at, metadata_json
		  FROM s3_multipart_uploads
		 WHERE bucket_id = ?
		   AND (? = '' OR key LIKE ? || '%')
		   AND (? = '' OR (key, upload_id) > (?, ?))
		 ORDER BY key, upload_id
		 LIMIT ? + 1
	`, bucketID, prefix, prefix, markerKey, markerKey, markerUploadID, limit)
	if err != nil {
		return nil, fmt.Errorf("s3_multipart_uploads: list paginated: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []S3MultipartUpload
	for rows.Next() {
		var u S3MultipartUpload
		var initiated string
		var userID, s3KeyID sql.NullInt64
		if err := rows.Scan(&u.ID, &u.UploadID, &u.BucketID, &u.Key,
			&userID, &s3KeyID, &initiated, &u.MetadataJSON); err != nil {
			return nil, fmt.Errorf("s3_multipart_uploads: scan paginated: %w", err)
		}
		if userID.Valid {
			v := userID.Int64
			u.InitiatedByUserID = &v
		}
		if s3KeyID.Valid {
			v := s3KeyID.Int64
			u.InitiatedByS3KeyID = &v
		}
		u.InitiatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", initiated)
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListPartsPaginated returns up to (limit+1) parts for uploadID whose
// part_number is strictly greater than markerPartNumber, ordered ascending.
//
// Cursor semantics: pass markerPartNumber=0 for the first page. On subsequent
// pages pass the part_number of the LAST INCLUDED part from the previous
// page. The SQL uses `part_number > ?` so the marker row itself is excluded.
//
// Limit clamping: values <=0 or >1000 collapse to the AWS-spec default of
// 1000. The SQL applies LIMIT (?+1) so callers can detect truncation by
// comparing len(parts) > limit.
//
// All inputs are SQL bind parameters.
func (r *S3MultipartRepo) ListPartsPaginated(ctx context.Context, uploadID string, markerPartNumber int, limit int) ([]S3MultipartPart, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, upload_id, part_number, size_bytes, md5, uploaded_at
		  FROM s3_multipart_parts
		 WHERE upload_id = ? AND part_number > ?
		 ORDER BY part_number ASC
		 LIMIT ? + 1
	`, uploadID, markerPartNumber, limit)
	if err != nil {
		return nil, fmt.Errorf("s3_multipart_parts: list paginated: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []S3MultipartPart
	for rows.Next() {
		var p S3MultipartPart
		var uploaded string
		if err := rows.Scan(&p.ID, &p.UploadID, &p.PartNumber, &p.SizeBytes, &p.MD5, &uploaded); err != nil {
			return nil, fmt.Errorf("s3_multipart_parts: scan paginated: %w", err)
		}
		p.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteUpload drops the upload row. ON DELETE CASCADE removes part rows.
func (r *S3MultipartRepo) DeleteUpload(ctx context.Context, tx *sql.Tx, uploadID string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM s3_multipart_uploads WHERE upload_id = ?`, uploadID,
	); err != nil {
		return fmt.Errorf("s3_multipart_uploads: delete: %w", err)
	}
	return nil
}

// ListStaleUploads returns uploads whose initiated_at is older than cutoff.
// Used by the daily cleanup job and the boot-recovery sweeper.
//
// Both attribution columns are scanned via sql.NullInt64 (post-036 NULLable).
func (r *S3MultipartRepo) ListStaleUploads(ctx context.Context, cutoff time.Time) ([]S3MultipartUpload, error) {
	cutoffStr := cutoff.UTC().Format("2006-01-02T15:04:05.000Z")
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, upload_id, bucket_id, key, initiated_by_user_id, initiated_by_s3_key_id, initiated_at, metadata_json
		FROM s3_multipart_uploads WHERE initiated_at < ?
		ORDER BY initiated_at ASC
	`, cutoffStr)
	if err != nil {
		return nil, fmt.Errorf("s3_multipart_uploads: list stale: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []S3MultipartUpload
	for rows.Next() {
		var u S3MultipartUpload
		var initiated string
		var userID, s3KeyID sql.NullInt64
		if err := rows.Scan(&u.ID, &u.UploadID, &u.BucketID, &u.Key, &userID, &s3KeyID, &initiated, &u.MetadataJSON); err != nil {
			return nil, fmt.Errorf("s3_multipart_uploads: scan stale: %w", err)
		}
		if userID.Valid {
			v := userID.Int64
			u.InitiatedByUserID = &v
		}
		if s3KeyID.Valid {
			v := s3KeyID.Int64
			u.InitiatedByS3KeyID = &v
		}
		u.InitiatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", initiated)
		out = append(out, u)
	}
	return out, rows.Err()
}
