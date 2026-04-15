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
type DockerTagsRepo struct{ db *DB }

// NewDockerTagsRepo constructs a repo bound to db.
func NewDockerTagsRepo(db *DB) *DockerTagsRepo { return &DockerTagsRepo{db: db} }

// Upsert writes (repo_id, tag) -> digest, returning the previous digest
// or "" if the tag did not exist or previously pointed to the same
// digest. SELECT-then-INSERT-OR-REPLACE pattern (Pitfall 1) for simplicity.
func (r *DockerTagsRepo) Upsert(ctx context.Context, tx *sql.Tx, repoID int64, tag, digest string) (priorDigest string, err error) {
	var prior sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT digest FROM docker_tags WHERE repo_id=? AND tag=?`,
		repoID, tag,
	).Scan(&prior)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prior = sql.NullString{Valid: false}
	case err != nil:
		return "", fmt.Errorf("docker_tags: probe: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO docker_tags(repo_id, tag, digest, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(repo_id, tag) DO UPDATE
		    SET digest = excluded.digest, updated_at = CURRENT_TIMESTAMP
	`, repoID, tag, digest); err != nil {
		return "", fmt.Errorf("docker_tags: upsert: %w", err)
	}

	if prior.Valid && prior.String != digest {
		return prior.String, nil
	}
	return "", nil
}

// Resolve returns the digest bound to tag, or ("", nil) if absent.
func (r *DockerTagsRepo) Resolve(ctx context.Context, repoID int64, tag string) (string, error) {
	var d string
	err := r.db.Reader.QueryRowContext(ctx,
		`SELECT digest FROM docker_tags WHERE repo_id=? AND tag=?`,
		repoID, tag,
	).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("docker_tags: resolve: %w", err)
	}
	return d, nil
}

// List returns all tag names in repoID, sorted ascending.
func (r *DockerTagsRepo) List(ctx context.Context, repoID int64) ([]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx,
		`SELECT tag FROM docker_tags WHERE repo_id=? ORDER BY tag ASC`, repoID,
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

// Delete removes (repo_id, tag), returning the digest it used to point
// at (or "" if absent). Returning the digest lets the caller DecRef in
// the same tx.
func (r *DockerTagsRepo) Delete(ctx context.Context, tx *sql.Tx, repoID int64, tag string) (digest string, err error) {
	var d sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT digest FROM docker_tags WHERE repo_id=? AND tag=?`,
		repoID, tag,
	).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("docker_tags: probe: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM docker_tags WHERE repo_id=? AND tag=?`, repoID, tag,
	); err != nil {
		return "", fmt.Errorf("docker_tags: delete: %w", err)
	}
	return d.String, nil
}
