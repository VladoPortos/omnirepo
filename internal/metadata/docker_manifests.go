package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DockerManifestsRepo owns docker_manifests rows. Manifests are stored as
// opaque BLOBs so the docker-content-digest header survives byte-for-byte
// round-trip (Pitfall 5).
type DockerManifestsRepo struct{ db *DB }

// DockerManifest is the in-memory projection.
type DockerManifest struct {
	RepoID    int64
	Digest    string
	MediaType string
	Body      []byte
	SizeBytes int64
	RefCount  int64
}

// ErrManifestDigestConflict is returned by Insert when (repo_id, digest)
// exists and the caller-supplied body does not match the stored bytes —
// which would violate OCI's content-addressed invariant.
var ErrManifestDigestConflict = errors.New("docker_manifests: digest conflict")

// NewDockerManifestsRepo constructs a repo bound to db.
func NewDockerManifestsRepo(db *DB) *DockerManifestsRepo { return &DockerManifestsRepo{db: db} }

// Insert stores a manifest. If (repo_id, digest) already exists, this is a
// no-op unless the bytes differ — then ErrManifestDigestConflict is
// returned (body mismatch with same digest = hash collision or caller
// bug). Caller is responsible for IncRef on referenced blobs in the same
// tx.
func (r *DockerManifestsRepo) Insert(ctx context.Context, tx *sql.Tx, repoID int64, digest, mediaType string, body []byte) error {
	// Check for existing row first (SQLite's INSERT OR IGNORE can't tell us
	// whether bodies match and we need that signal).
	var existing []byte
	err := tx.QueryRowContext(ctx,
		`SELECT body FROM docker_manifests WHERE repo_id=? AND digest=?`,
		repoID, digest,
	).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// fall through to insert
	case err != nil:
		return fmt.Errorf("docker_manifests: probe: %w", err)
	default:
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("%w: repo=%d digest=%s", ErrManifestDigestConflict, repoID, digest)
		}
		return nil
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO docker_manifests(repo_id, digest, media_type, body, size_bytes, ref_count)
		VALUES (?, ?, ?, ?, ?, 0)
	`, repoID, digest, mediaType, body, int64(len(body)))
	if err != nil {
		return fmt.Errorf("docker_manifests: insert: %w", err)
	}
	return nil
}

// GetByDigest returns the manifest, or (nil, nil) if absent.
func (r *DockerManifestsRepo) GetByDigest(ctx context.Context, repoID int64, digest string) (*DockerManifest, error) {
	var m DockerManifest
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT repo_id, digest, media_type, body, size_bytes, ref_count
		FROM docker_manifests WHERE repo_id=? AND digest=?
	`, repoID, digest).Scan(&m.RepoID, &m.Digest, &m.MediaType, &m.Body, &m.SizeBytes, &m.RefCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("docker_manifests: get: %w", err)
	}
	return &m, nil
}

// GetByDigestTx is GetByDigest but reads via the caller-supplied tx. Use this
// from inside a WriteTx callback where a Reader-pool read would deadlock on
// writer lock contention (SQLite serializes reader against in-flight writer
// tx when the reader pool races a commit). Phase 02-07 manifest PUT uses
// this to look up the prior manifest body for refcount delta.
func (r *DockerManifestsRepo) GetByDigestTx(ctx context.Context, tx *sql.Tx, repoID int64, digest string) (*DockerManifest, error) {
	var m DockerManifest
	err := tx.QueryRowContext(ctx, `
		SELECT repo_id, digest, media_type, body, size_bytes, ref_count
		FROM docker_manifests WHERE repo_id=? AND digest=?
	`, repoID, digest).Scan(&m.RepoID, &m.Digest, &m.MediaType, &m.Body, &m.SizeBytes, &m.RefCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("docker_manifests: get tx: %w", err)
	}
	return &m, nil
}

// IncRef increments ref_count (for index manifests that reference child
// manifests). Errors if the row is missing.
func (r *DockerManifestsRepo) IncRef(ctx context.Context, tx *sql.Tx, repoID int64, digest string) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE docker_manifests SET ref_count = ref_count + 1
		WHERE repo_id=? AND digest=?
	`, repoID, digest)
	if err != nil {
		return fmt.Errorf("docker_manifests: incref: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("docker_manifests: incref: missing (repo=%d digest=%s)", repoID, digest)
	}
	return nil
}

// DecRef decrements ref_count. Errors (without going negative) if the row
// is missing or ref_count already 0.
func (r *DockerManifestsRepo) DecRef(ctx context.Context, tx *sql.Tx, repoID int64, digest string) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE docker_manifests SET ref_count = ref_count - 1
		WHERE repo_id=? AND digest=? AND ref_count > 0
	`, repoID, digest)
	if err != nil {
		return fmt.Errorf("docker_manifests: decref: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: manifest (repo=%d digest=%s)", ErrRefCountUnderflow, repoID, digest)
	}
	return nil
}

// Delete removes the manifest row. Caller is responsible for decrementing
// refcounts of referenced blobs beforehand.
func (r *DockerManifestsRepo) Delete(ctx context.Context, tx *sql.Tx, repoID int64, digest string) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM docker_manifests WHERE repo_id=? AND digest=?
	`, repoID, digest)
	if err != nil {
		return fmt.Errorf("docker_manifests: delete: %w", err)
	}
	return nil
}
