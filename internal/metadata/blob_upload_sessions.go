package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BlobUploadSessionsRepo owns blob_upload_sessions — the per-UUID
// chunked-upload state tracked by the /v2 handler between POST and PUT.
// Distinct from BlobUploadsRepo (digest-keyed GC exclusion set).
type BlobUploadSessionsRepo struct{ db *DB }

// BlobUploadSession is the in-memory projection.
type BlobUploadSession struct {
	UUID        string
	RepoID      int64
	BytesSoFar  int64
	StartedAt   time.Time
	LastPatchAt time.Time
	ExpiresAt   time.Time
}

// NewBlobUploadSessionsRepo constructs a repo bound to db.
func NewBlobUploadSessionsRepo(db *DB) *BlobUploadSessionsRepo {
	return &BlobUploadSessionsRepo{db: db}
}

// Create inserts a new session row. ttl is the session's lifetime (spec
// default 1h); expires_at = now + ttl.
func (r *BlobUploadSessionsRepo) Create(ctx context.Context, tx *sql.Tx, uuid string, repoID int64, ttl time.Duration) error {
	expires := time.Now().Add(ttl).UTC()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO blob_upload_sessions(uuid, repo_id, bytes_so_far, started_at, last_patch_at, expires_at)
		VALUES (?, ?, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)
	`, uuid, repoID, expires)
	if err != nil {
		return fmt.Errorf("blob_upload_sessions: create %s: %w", uuid, err)
	}
	return nil
}

// AppendBytes advances bytes_so_far by n and bumps last_patch_at.
func (r *BlobUploadSessionsRepo) AppendBytes(ctx context.Context, tx *sql.Tx, uuid string, n int64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE blob_upload_sessions
		SET bytes_so_far = bytes_so_far + ?, last_patch_at = CURRENT_TIMESTAMP
		WHERE uuid = ?
	`, n, uuid)
	if err != nil {
		return fmt.Errorf("blob_upload_sessions: append %s: %w", uuid, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("blob_upload_sessions: append %s: missing", uuid)
	}
	return nil
}

// Lookup returns the session row, or (nil, nil) if absent.
func (r *BlobUploadSessionsRepo) Lookup(ctx context.Context, uuid string) (*BlobUploadSession, error) {
	var s BlobUploadSession
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT uuid, repo_id, bytes_so_far, started_at, last_patch_at, expires_at
		FROM blob_upload_sessions WHERE uuid = ?
	`, uuid).Scan(&s.UUID, &s.RepoID, &s.BytesSoFar, &s.StartedAt, &s.LastPatchAt, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("blob_upload_sessions: lookup %s: %w", uuid, err)
	}
	return &s, nil
}

// Delete removes the row. Safe on missing rows (no error).
func (r *BlobUploadSessionsRepo) Delete(ctx context.Context, tx *sql.Tx, uuid string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM blob_upload_sessions WHERE uuid = ?`, uuid)
	if err != nil {
		return fmt.Errorf("blob_upload_sessions: delete %s: %w", uuid, err)
	}
	return nil
}

// PruneExpiredReturning deletes every row with expires_at < now and
// returns the uuids of the removed rows. Used by the GC handler so the
// caller can also remove the per-uuid tmp upload file at
// <DataRoot>/tmp/uploads/<uuid>.
func (r *BlobUploadSessionsRepo) PruneExpiredReturning(ctx context.Context, tx *sql.Tx, now time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`DELETE FROM blob_upload_sessions WHERE expires_at < ? RETURNING uuid`, now.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("blob_upload_sessions: prune returning: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("blob_upload_sessions: prune scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
