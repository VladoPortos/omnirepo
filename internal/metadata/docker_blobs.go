package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DockerBlobsRepo owns docker_blobs rows: the CAS refcount table used by
// the OCI /v2 handler (D-02) and the GC sweeper (D-38).
//
// Tx-taking methods accept *sql.Tx directly so callers can bundle
// refcount mutations into the same writer tx as manifest inserts/deletes
// (phase 2 invariant: refcount moves with the row that references the
// blob, never lazily).
type DockerBlobsRepo struct{ db *DB }

// DockerBlob is the in-memory projection of a docker_blobs row.
type DockerBlob struct {
	Digest        string
	SizeBytes     int64
	RefCount      int64
	LastTouchedAt time.Time
}

// NewDockerBlobsRepo constructs a repo bound to db.
func NewDockerBlobsRepo(db *DB) *DockerBlobsRepo { return &DockerBlobsRepo{db: db} }

// ErrRefCountUnderflow is returned by DecRef when the blob's ref_count is
// already 0. Callers MUST bubble this as an invariant violation — never
// swallow.
var ErrRefCountUnderflow = errors.New("docker_blobs: ref_count underflow")

// UpsertZeroRef inserts the blob at ref_count=0 if absent; no-op if the
// row already exists. Idempotent — safe to call unconditionally on a
// streamed blob PUT before the manifest that references it lands.
func (r *DockerBlobsRepo) UpsertZeroRef(ctx context.Context, tx *sql.Tx, digest string, size int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO docker_blobs(digest, size_bytes, ref_count, last_touched_at)
		VALUES (?, ?, 0, CURRENT_TIMESTAMP)
	`, digest, size)
	if err != nil {
		return fmt.Errorf("docker_blobs: upsert %s: %w", digest, err)
	}
	return nil
}

// IncRef increments ref_count for digest. Returns an error if the row is
// missing (caller must UpsertZeroRef first).
func (r *DockerBlobsRepo) IncRef(ctx context.Context, tx *sql.Tx, digest string) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE docker_blobs
		SET ref_count = ref_count + 1, last_touched_at = CURRENT_TIMESTAMP
		WHERE digest = ?
	`, digest)
	if err != nil {
		return fmt.Errorf("docker_blobs: incref %s: %w", digest, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("docker_blobs: incref %s: row missing", digest)
	}
	return nil
}

// DecRef decrements ref_count for digest. Returns ErrRefCountUnderflow if
// the row is missing or ref_count is already 0 (T-02-01-03 guard).
func (r *DockerBlobsRepo) DecRef(ctx context.Context, tx *sql.Tx, digest string) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE docker_blobs
		SET ref_count = ref_count - 1, last_touched_at = CURRENT_TIMESTAMP
		WHERE digest = ? AND ref_count > 0
	`, digest)
	if err != nil {
		return fmt.Errorf("docker_blobs: decref %s: %w", digest, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrRefCountUnderflow, digest)
	}
	return nil
}

// Touch bumps last_touched_at to CURRENT_TIMESTAMP without changing
// ref_count. Used by cross-repo mount and resumed-upload paths to keep a
// recently-seen blob out of the GC quiescence window.
func (r *DockerBlobsRepo) Touch(ctx context.Context, tx *sql.Tx, digest string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE docker_blobs SET last_touched_at = CURRENT_TIMESTAMP WHERE digest = ?
	`, digest)
	if err != nil {
		return fmt.Errorf("docker_blobs: touch %s: %w", digest, err)
	}
	return nil
}

// Stat returns the row for digest, or (nil, nil) if absent.
func (r *DockerBlobsRepo) Stat(ctx context.Context, digest string) (*DockerBlob, error) {
	var b DockerBlob
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT digest, size_bytes, ref_count, last_touched_at
		FROM docker_blobs WHERE digest = ?
	`, digest).Scan(&b.Digest, &b.SizeBytes, &b.RefCount, &b.LastTouchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("docker_blobs: stat %s: %w", digest, err)
	}
	return &b, nil
}

// GCCandidates returns blobs eligible for GC: ref_count==0 AND
// last_touched_at < now-quiescence. Caller is responsible for the
// blob_uploads exclusion join (D-03, D-38).
func (r *DockerBlobsRepo) GCCandidates(ctx context.Context, quiescence time.Duration) ([]DockerBlob, error) {
	// datetime('now', '-N seconds') is the modernc/sqlite-friendly form.
	modifier := fmt.Sprintf("-%d seconds", int64(quiescence.Seconds()))
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT digest, size_bytes, ref_count, last_touched_at
		FROM docker_blobs
		WHERE ref_count = 0
		  AND last_touched_at < datetime('now', ?)
	`, modifier)
	if err != nil {
		return nil, fmt.Errorf("docker_blobs: gc candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DockerBlob
	for rows.Next() {
		var b DockerBlob
		if err := rows.Scan(&b.Digest, &b.SizeBytes, &b.RefCount, &b.LastTouchedAt); err != nil {
			return nil, fmt.Errorf("docker_blobs: gc scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Delete removes the blob row. Caller is responsible for deleting the CAS
// file first (GC sweep order: file then row, per D-38).
func (r *DockerBlobsRepo) Delete(ctx context.Context, tx *sql.Tx, digest string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM docker_blobs WHERE digest = ?`, digest)
	if err != nil {
		return fmt.Errorf("docker_blobs: delete %s: %w", digest, err)
	}
	return nil
}
