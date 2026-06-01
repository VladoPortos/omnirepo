package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BlobUploadsRepo owns the blob_uploads table.
// Start inserts (digest, expires_at=now+ttl); Complete deletes the row;
// PruneExpired deletes rows with expires_at < now. GC consumes this
// to exclude in-flight digests from sweep.
//
// Start is idempotent: a second Start for the same digest updates expires_at
// to the newer value instead of returning a PK conflict. This keeps resumable
// uploads simple without forcing callers to catch conflict errors.
type BlobUploadsRepo struct{ db *DB }

// NewBlobUploadsRepo constructs a repo bound to db.
func NewBlobUploadsRepo(db *DB) *BlobUploadsRepo { return &BlobUploadsRepo{db: db} }

// Start registers an in-flight upload for digest with the given ttl. The ttl
// may be negative (used by tests to seed an already-expired row).
func (r *BlobUploadsRepo) Start(ctx context.Context, digest string, ttl time.Duration) error {
	expires := time.Now().Add(ttl).UTC()
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO blob_uploads(digest, expires_at) VALUES (?, ?)
			ON CONFLICT(digest) DO UPDATE SET expires_at=excluded.expires_at
		`, digest, expires)
		if err != nil {
			return fmt.Errorf("blob_uploads: start %s: %w", digest, err)
		}
		return nil
	})
}

// Complete removes the in-flight marker for digest. No-ops if missing.
func (r *BlobUploadsRepo) Complete(ctx context.Context, digest string) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM blob_uploads WHERE digest=?`, digest)
		if err != nil {
			return fmt.Errorf("blob_uploads: complete %s: %w", digest, err)
		}
		return nil
	})
}

// Active returns the digests of every in-flight blob_uploads row whose
// expires_at is still in the future. Used by the GC handler to snapshot
// the exclusion set BEFORE iterating GC candidates — this is the
// race-proof guarantee: any digest the PUT path has already registered
// cannot be deleted by the same GC run.
//
// The reader pool is fine here: the GC handler tolerates the minor
// snapshot staleness within the same run because the OCI PUT path
// registers its digest BEFORE cas.PutFromPath, so a digest visible on
// disk at GC time is guaranteed visible in blob_uploads at the moment
// the snapshot is taken (or it was already promoted to docker_blobs and
// is protected by ref_count or quiescence instead).
func (r *BlobUploadsRepo) Active(ctx context.Context) ([]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT digest FROM blob_uploads WHERE expires_at >= ?
	`, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("blob_uploads: active: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("blob_uploads: active scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneExpired deletes every row with expires_at < now. Returns the number of
// rows removed.
func (r *BlobUploadsRepo) PruneExpired(ctx context.Context, now time.Time) (int, error) {
	var removed int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM blob_uploads WHERE expires_at<?`, now.UTC())
		if err != nil {
			return fmt.Errorf("blob_uploads: prune: %w", err)
		}
		removed, _ = res.RowsAffected()
		return nil
	})
	return int(removed), err
}
