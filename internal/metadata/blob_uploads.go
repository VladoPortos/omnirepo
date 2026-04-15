package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BlobUploadsRepo is the Phase 1 stub for the blob_uploads table (D-30).
// Start inserts (digest, expires_at=now+ttl); Complete deletes the row;
// PruneExpired deletes rows with expires_at < now. Phase 2 GC consumes this
// to exclude in-flight digests from sweep.
//
// Start is idempotent: a second Start for the same digest updates expires_at
// to the newer value instead of returning a PK conflict. This keeps resumable
// uploads simple for Phase 2 without forcing callers to catch conflict errors.
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
