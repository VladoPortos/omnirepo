// Package metadata — S3KeysRepo owns the per-project S3 access-key table.
// The AWS SigV4 verifier looks up the key
// by AKID on every signed request, so the hot path is FindByAKID — which
// MUST collapse "missing" and "revoked" into a single ErrS3AccessKeyNotFound
// to avoid leaking a revocation oracle.
//
// `secret_enc` stores the AES-GCM-sealed secret — see internal/crypto/aead.go.
// This package carries the sealed bytes opaquely; decryption is the SigV4
// verifier's concern.
package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrS3AccessKeyNotFound is returned by S3KeysRepo.FindByAKID when no
// live (non-revoked) row matches. Revoked rows collapse to this error too
// so attackers cannot probe historical AKID existence.
var ErrS3AccessKeyNotFound = errors.New("metadata: s3 access key not found or revoked")

// S3AccessKey mirrors one s3_access_keys row.
type S3AccessKey struct {
	ID              int64
	ProjectID       int64
	AccessKeyID     string
	SecretEnc       []byte // opaque AEAD-sealed bytes; callers decrypt via internal/crypto
	Label           string
	CreatedByUserID int64
	CreatedAt       time.Time
	LastUsedAt      *time.Time
	RevokedAt       *time.Time
}

// S3KeysRepo is the typed repo for s3_access_keys. Writers ride in the
// caller's *sql.Tx so the INSERT lands alongside any project-level audit
// event in one writer transaction.
type S3KeysRepo struct{ db *DB }

// NewS3KeysRepo constructs the repo bound to db.
func NewS3KeysRepo(db *DB) *S3KeysRepo { return &S3KeysRepo{db: db} }

// Insert writes a new row. `secretEnc` is the sealed secret blob (the
// caller owns AEAD encryption). Returns the new row id. Uniqueness on
// `access_key_id` surfaces as a driver-native UNIQUE error that callers
// can unwrap for typed 409 mapping.
func (r *S3KeysRepo) Insert(ctx context.Context, tx *sql.Tx, row *S3AccessKey) (int64, error) {
	if row == nil {
		return 0, errors.New("s3_access_keys: nil row")
	}
	if row.AccessKeyID == "" || len(row.SecretEnc) == 0 || row.ProjectID == 0 || row.CreatedByUserID == 0 {
		return 0, errors.New("s3_access_keys: project_id, access_key_id, secret_enc, created_by_user_id required")
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO s3_access_keys(project_id, access_key_id, secret_enc, label, created_by_user_id)
		VALUES (?, ?, ?, ?, ?)
	`, row.ProjectID, row.AccessKeyID, row.SecretEnc, row.Label, row.CreatedByUserID)
	if err != nil {
		return 0, fmt.Errorf("s3_access_keys: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("s3_access_keys: last insert id: %w", err)
	}
	return id, nil
}

// FindByAKID returns the live (non-revoked) key with the matching AKID whose
// owning project is also live. Collapses missing + revoked + deleted-project
// into ErrS3AccessKeyNotFound (no-oracle).
//
// The INNER JOIN on projects with `p.deleted_at IS NULL` is a SECOND
// independent gate beyond the soft-delete cascade — even if a row was
// somehow missed by the cascade, or a new row appeared between project
// soft-delete and the cascade landing, the JOIN filter alone refuses to
// resolve a key whose parent project is dead. Belt-and-braces.
func (r *S3KeysRepo) FindByAKID(ctx context.Context, akid string) (*S3AccessKey, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT k.id, k.project_id, k.access_key_id, k.secret_enc, k.label, k.created_by_user_id,
		       k.created_at, k.last_used_at, k.revoked_at
		FROM s3_access_keys k
		INNER JOIN projects p ON p.id = k.project_id
		WHERE k.access_key_id = ? AND k.revoked_at IS NULL
		      AND p.deleted_at IS NULL
	`, akid)
	k, err := scanS3AccessKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrS3AccessKeyNotFound
		}
		return nil, fmt.Errorf("s3_access_keys: find by akid: %w", err)
	}
	return k, nil
}

// FindByID returns the key row by primary id, regardless of revoked state.
// Used by admin-scoped endpoints. Returns ErrNotFound when missing.
func (r *S3KeysRepo) FindByID(ctx context.Context, id int64) (*S3AccessKey, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, project_id, access_key_id, secret_enc, label, created_by_user_id,
		       created_at, last_used_at, revoked_at
		FROM s3_access_keys WHERE id = ?
	`, id)
	k, err := scanS3AccessKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3_access_keys: find by id: %w", err)
	}
	return k, nil
}

// ListByProject returns every non-revoked key for projectID, ordered by
// created_at ASC (oldest first, matching UI list conventions).
func (r *S3KeysRepo) ListByProject(ctx context.Context, projectID int64) ([]S3AccessKey, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, access_key_id, secret_enc, label, created_by_user_id,
		       created_at, last_used_at, revoked_at
		FROM s3_access_keys
		WHERE project_id = ? AND revoked_at IS NULL
		ORDER BY created_at ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("s3_access_keys: list by project: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []S3AccessKey
	for rows.Next() {
		k, err := scanS3AccessKey(rows)
		if err != nil {
			return nil, fmt.Errorf("s3_access_keys: scan: %w", err)
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// ListByCreatedByUser returns every non-revoked key that userID created,
// across any project, ordered by created_at DESC (newest first — matches
// the profile-page UX where the user wants to see the most recently
// minted key at the top).
func (r *S3KeysRepo) ListByCreatedByUser(ctx context.Context, userID int64) ([]S3AccessKey, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, access_key_id, secret_enc, label, created_by_user_id,
		       created_at, last_used_at, revoked_at
		FROM s3_access_keys
		WHERE created_by_user_id = ? AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("s3_access_keys: list by created_by_user: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []S3AccessKey
	for rows.Next() {
		k, err := scanS3AccessKey(rows)
		if err != nil {
			return nil, fmt.Errorf("s3_access_keys: scan: %w", err)
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// TouchLastUsed bumps last_used_at = now() for the AKID. Best-effort: the
// caller typically invokes this inside a short-lived goroutine so a hot
// SigV4 verify path never blocks on a writer-pool contention.
//
// Returns nil for a missing or revoked AKID (no error) — this is a
// telemetry path, not a correctness path, so we do not surface oracle-y
// errors to the caller.
func (r *S3KeysRepo) TouchLastUsed(ctx context.Context, akid string) error {
	_, err := r.db.Writer.ExecContext(ctx, `
		UPDATE s3_access_keys
		   SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE access_key_id = ? AND revoked_at IS NULL
	`, akid)
	if err != nil {
		return fmt.Errorf("s3_access_keys: touch last_used: %w", err)
	}
	return nil
}

// RevokeIfOwnedBy stamps revoked_at = now() for the key id ONLY when
// (a) the row exists, (b) it was created by ownerUserID, and (c) it is
// not already revoked. Returns true when a row was actually updated so
// callers can collapse "missing", "already revoked", and "owned by
// somebody else" into a single 404 without a separate FindByID +
// ownership check (TOCTOU-safe vs concurrent revokes / re-inserts).
func (r *S3KeysRepo) RevokeIfOwnedBy(ctx context.Context, tx *sql.Tx, id, ownerUserID int64) (bool, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE s3_access_keys
		   SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?
		   AND created_by_user_id = ?
		   AND revoked_at IS NULL
	`, id, ownerUserID)
	if err != nil {
		return false, fmt.Errorf("s3_access_keys: revoke-if-owned %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("s3_access_keys: revoke-if-owned rows %d: %w", id, err)
	}
	return n > 0, nil
}

// RevokeAllForProject stamps revoked_at = cascadeTS on every live (non-revoked)
// s3_access_keys row for projectID. Used by ProjectsRepo.SoftDelete to cascade
// access-key revocation atomically inside the project's WriteTx. Returns the
// number of rows updated.
//
// cascadeTS is supplied by the caller — typically the literal string read back
// from `projects.deleted_at` after the project's own UPDATE — so on Restore the
// reverse helper can identify exactly which rows were cascade-revoked by
// THIS soft-delete (timestamp-equality cascade marker).
//
// Idempotent: rows already revoked (revoked_at IS NOT NULL) are left untouched
// — including rows independently revoked at a different timestamp before the
// cascade fired.
//
// NOTE: this column is conventionally written via strftime ISO-8601-ms, but
// SQLite TEXT columns accept any string. We deliberately store the project's
// own deleted_at format (YYYY-MM-DD HH:MM:SS from CURRENT_TIMESTAMP) here so
// equality compare on Restore is byte-for-byte exact against the project row.
func (r *S3KeysRepo) RevokeAllForProject(ctx context.Context, tx *sql.Tx, projectID int64, cascadeTS string) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE s3_access_keys
		   SET revoked_at = ?
		 WHERE project_id = ? AND revoked_at IS NULL
	`, cascadeTS, projectID)
	if err != nil {
		return 0, fmt.Errorf("s3_access_keys: revoke all for project %d: %w", projectID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("s3_access_keys: revoke all rows %d: %w", projectID, err)
	}
	return n, nil
}

// RestoreCascadedForProject reverses a cascade revoke initiated by
// RevokeAllForProject(projectID, priorTS). Only rows whose revoked_at exactly
// equals priorTS are cleared — independently revoked rows have a different
// timestamp and are left alone. Returns the number of rows updated.
func (r *S3KeysRepo) RestoreCascadedForProject(ctx context.Context, tx *sql.Tx, projectID int64, priorTS string) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE s3_access_keys
		   SET revoked_at = NULL
		 WHERE project_id = ? AND revoked_at = ?
	`, projectID, priorTS)
	if err != nil {
		return 0, fmt.Errorf("s3_access_keys: restore cascaded for project %d: %w", projectID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("s3_access_keys: restore cascaded rows %d: %w", projectID, err)
	}
	return n, nil
}

// Revoke stamps revoked_at = now() for the key id inside the caller's tx.
// Idempotent: re-revoking a revoked row is a no-op.
func (r *S3KeysRepo) Revoke(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE s3_access_keys
		   SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND revoked_at IS NULL
	`, id); err != nil {
		return fmt.Errorf("s3_access_keys: revoke %d: %w", id, err)
	}
	return nil
}

func scanS3AccessKey(rs scanner) (*S3AccessKey, error) {
	var k S3AccessKey
	var created string
	var lastUsed, revoked sql.NullString
	if err := rs.Scan(
		&k.ID, &k.ProjectID, &k.AccessKeyID, &k.SecretEnc, &k.Label, &k.CreatedByUserID,
		&created, &lastUsed, &revoked,
	); err != nil {
		return nil, err
	}
	k.CreatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", created)
	if lastUsed.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05.000Z", lastUsed.String)
		k.LastUsedAt = &t
	}
	if revoked.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05.000Z", revoked.String)
		k.RevokedAt = &t
	}
	return &k, nil
}
