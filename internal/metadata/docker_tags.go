package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DockerTagsRepo owns docker_tags rows. Upsert returns the prior digest
// (if any) so the caller can DecRef the manifest/blobs it no longer
// points at (Pitfall 1).
//
// The image column (migration 021) scopes tags within a repo so the same
// OmniRepo repo can host multiple OCI "images" — most notably Helm charts,
// where the Helm CLI always appends the chart name as a 4th path segment.
// For classic single-image Docker repos, image is the empty string.
type DockerTagsRepo struct{ db *DB }

// NewDockerTagsRepo constructs a repo bound to db.
func NewDockerTagsRepo(db *DB) *DockerTagsRepo { return &DockerTagsRepo{db: db} }

// Upsert writes (repo_id, image, tag) -> digest, returning the previous
// digest or "" if the tag did not exist or previously pointed to the same
// digest. SELECT-then-INSERT-OR-REPLACE pattern (Pitfall 1) for simplicity.
func (r *DockerTagsRepo) Upsert(ctx context.Context, tx *sql.Tx, repoID int64, image, tag, digest string) (priorDigest string, err error) {
	var prior sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT digest FROM docker_tags WHERE repo_id=? AND image=? AND tag=?`,
		repoID, image, tag,
	).Scan(&prior)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prior = sql.NullString{Valid: false}
	case err != nil:
		return "", fmt.Errorf("docker_tags: probe: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO docker_tags(repo_id, image, tag, digest, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(repo_id, image, tag) DO UPDATE
		    SET digest = excluded.digest, updated_at = CURRENT_TIMESTAMP
	`, repoID, image, tag, digest); err != nil {
		return "", fmt.Errorf("docker_tags: upsert: %w", err)
	}

	if prior.Valid && prior.String != digest {
		return prior.String, nil
	}
	return "", nil
}

// Resolve returns the digest bound to (image, tag) within repoID, or
// ("", nil) if absent.
func (r *DockerTagsRepo) Resolve(ctx context.Context, repoID int64, image, tag string) (string, error) {
	var d string
	err := r.db.Reader.QueryRowContext(ctx,
		`SELECT digest FROM docker_tags WHERE repo_id=? AND image=? AND tag=?`,
		repoID, image, tag,
	).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("docker_tags: resolve: %w", err)
	}
	return d, nil
}

// List returns all tag names in (repoID, image), sorted ascending.
func (r *DockerTagsRepo) List(ctx context.Context, repoID int64, image string) ([]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx,
		`SELECT tag FROM docker_tags WHERE repo_id=? AND image=? ORDER BY tag ASC`,
		repoID, image,
	)
	if err != nil {
		return nil, fmt.Errorf("docker_tags: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("docker_tags: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListPaginated returns tag names in (repoID, image) strictly greater than
// `after` (lexicographic), sorted ascending, capped at `limit`. An empty
// `after` returns the first page. Limit is clamped to [1, 1000].
func (r *DockerTagsRepo) ListPaginated(ctx context.Context, repoID int64, image string, limit int, after string) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT tag FROM docker_tags
		WHERE repo_id = ? AND image = ? AND tag > ?
		ORDER BY tag ASC
		LIMIT ?
	`, repoID, image, after, limit)
	if err != nil {
		return nil, fmt.Errorf("docker_tags: list paginated: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("docker_tags: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ExistsTag returns true if (repoID, image, tag) has a row. Used by the
// cosign badge check (D-08): tag "sha256-<hex>.sig" presence → signed=true.
func (r *DockerTagsRepo) ExistsTag(ctx context.Context, repoID int64, image, tag string) (bool, error) {
	var one int
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT 1 FROM docker_tags WHERE repo_id = ? AND image = ? AND tag = ? LIMIT 1
	`, repoID, image, tag).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("docker_tags: exists: %w", err)
	}
	return true, nil
}

// CountForDigest returns the number of tag rows in repoID pointing at digest.
// Used by manifest DELETE to decide whether deleting a single tag reference
// should cascade into ref_count decrements on the manifest's referenced blobs.
// This count is intentionally image-blind: ref_count on a manifest is the
// total tag-reference count across every image in the repo.
func (r *DockerTagsRepo) CountForDigest(ctx context.Context, repoID int64, digest string) (int64, error) {
	var n int64
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM docker_tags WHERE repo_id = ? AND digest = ?
	`, repoID, digest).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("docker_tags: count: %w", err)
	}
	return n, nil
}

// CountForDigestTx is CountForDigest called through a caller-supplied tx.
// Required inside WriteTx callbacks where Reader-pool reads race the
// in-flight writer tx and can deadlock (see GetByDigestTx rationale).
func (r *DockerTagsRepo) CountForDigestTx(ctx context.Context, tx *sql.Tx, repoID int64, digest string) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM docker_tags WHERE repo_id = ? AND digest = ?
	`, repoID, digest).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("docker_tags: count tx: %w", err)
	}
	return n, nil
}

// Delete removes (repo_id, image, tag), returning the digest it used to
// point at (or "" if absent). Returning the digest lets the caller DecRef
// in the same tx.
func (r *DockerTagsRepo) Delete(ctx context.Context, tx *sql.Tx, repoID int64, image, tag string) (digest string, err error) {
	var d sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT digest FROM docker_tags WHERE repo_id=? AND image=? AND tag=?`,
		repoID, image, tag,
	).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("docker_tags: probe: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM docker_tags WHERE repo_id=? AND image=? AND tag=?`,
		repoID, image, tag,
	); err != nil {
		return "", fmt.Errorf("docker_tags: delete: %w", err)
	}
	return d.String, nil
}
