package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// APIKey mirrors one row of the api_keys table. OwnerKind is always "user" or
// "project" (CHECK constraint in schema); exactly one of OwnerUserID /
// OwnerProjectID is non-nil.
type APIKey struct {
	ID             int64
	OwnerKind      string // "user" | "project"
	OwnerUserID    *int64
	OwnerProjectID *int64
	Name           string
	TokenPrefix    string
	TokenSHA256    string
	LastUsedAt     *time.Time
	CreatedAt      time.Time
	RevokedAt      *time.Time
}

// APIKeysRepo owns CRUD on api_keys.
//
// Revocation semantics: FindByPrefixSha EXCLUDES revoked keys (revoked_at IS
// NULL) — see 01-04-SUMMARY §"decision on API-key revocation semantics".
// Callers who need to display revoked keys for audit will use a distinct
// method (not exposed in Phase 1). This makes the middleware path trivially
// "lookup-or-401" without a branch on revoked_at.
type APIKeysRepo struct{ db *DB }

// NewAPIKeysRepo constructs a repo bound to db.
func NewAPIKeysRepo(db *DB) *APIKeysRepo { return &APIKeysRepo{db: db} }

// CreateUserKey inserts an api_keys row owned by userID.
func (r *APIKeysRepo) CreateUserKey(ctx context.Context, userID int64, name, prefix, sha256hex string) (int64, error) {
	return r.create(ctx, "user", &userID, nil, name, prefix, sha256hex)
}

// CreateProjectKey inserts an api_keys row owned by projectID.
func (r *APIKeysRepo) CreateProjectKey(ctx context.Context, projectID int64, name, prefix, sha256hex string) (int64, error) {
	return r.create(ctx, "project", nil, &projectID, name, prefix, sha256hex)
}

func (r *APIKeysRepo) create(ctx context.Context, kind string, userID, projectID *int64, name, prefix, sha256hex string) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO api_keys(owner_kind, owner_user_id, owner_project_id, name, token_prefix, token_sha256)
			VALUES (?, ?, ?, ?, ?, ?)
		`, kind, userID, projectID, name, prefix, sha256hex)
		if execErr != nil {
			return fmt.Errorf("api_keys: create %s %q: %w", kind, name, execErr)
		}
		lid, lidErr := res.LastInsertId()
		if lidErr != nil {
			return fmt.Errorf("api_keys: last insert id: %w", lidErr)
		}
		id = lid
		return nil
	})
	return id, err
}

// FindByPrefixSha returns the live (non-revoked) api_keys row matching both
// prefix and sha256. Returns ErrNotFound on miss or revoked.
func (r *APIKeysRepo) FindByPrefixSha(ctx context.Context, prefix, sha256hex string) (*APIKey, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, owner_kind, owner_user_id, owner_project_id, name, token_prefix, token_sha256,
		       last_used_at, created_at, revoked_at
		FROM api_keys
		WHERE token_prefix=? AND token_sha256=? AND revoked_at IS NULL
	`, prefix, sha256hex)
	var k APIKey
	var userID, projectID sql.NullInt64
	var lastUsed, revoked sql.NullTime
	if err := row.Scan(&k.ID, &k.OwnerKind, &userID, &projectID, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
		&lastUsed, &k.CreatedAt, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("api_keys: scan: %w", err)
	}
	if userID.Valid {
		v := userID.Int64
		k.OwnerUserID = &v
	}
	if projectID.Valid {
		v := projectID.Int64
		k.OwnerProjectID = &v
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time
		k.RevokedAt = &t
	}
	return &k, nil
}

// FindByID returns the live (non-revoked) api_keys row with matching id.
// Returns ErrNotFound on miss or revoked. Used by the /v2 Bearer middleware
// to re-resolve an Actor from a JWT's claims on every request.
func (r *APIKeysRepo) FindByID(ctx context.Context, id int64) (*APIKey, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, owner_kind, owner_user_id, owner_project_id, name, token_prefix, token_sha256,
		       last_used_at, created_at, revoked_at
		FROM api_keys
		WHERE id=? AND revoked_at IS NULL
	`, id)
	var k APIKey
	var userID, projectID sql.NullInt64
	var lastUsed, revoked sql.NullTime
	if err := row.Scan(&k.ID, &k.OwnerKind, &userID, &projectID, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
		&lastUsed, &k.CreatedAt, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("api_keys: scan by id: %w", err)
	}
	if userID.Valid {
		v := userID.Int64
		k.OwnerUserID = &v
	}
	if projectID.Valid {
		v := projectID.Int64
		k.OwnerProjectID = &v
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time
		k.RevokedAt = &t
	}
	return &k, nil
}

// ListByUser returns all live (non-revoked) API keys owned by userID,
// ordered by created_at DESC. Used by the /api/v1/me/api-keys endpoint.
func (r *APIKeysRepo) ListByUser(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, owner_kind, owner_user_id, owner_project_id, name, token_prefix, token_sha256,
		       last_used_at, created_at, revoked_at
		FROM api_keys
		WHERE owner_kind='user' AND owner_user_id=? AND revoked_at IS NULL
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("api_keys: list by user %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var userID2, projectID sql.NullInt64
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&k.ID, &k.OwnerKind, &userID2, &projectID, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
			&lastUsed, &k.CreatedAt, &revoked); err != nil {
			return nil, fmt.Errorf("api_keys: list scan: %w", err)
		}
		if userID2.Valid {
			v := userID2.Int64
			k.OwnerUserID = &v
		}
		if projectID.Valid {
			v := projectID.Int64
			k.OwnerProjectID = &v
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			k.LastUsedAt = &t
		}
		if revoked.Valid {
			t := revoked.Time
			k.RevokedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ListByProject returns all live (non-revoked) API keys owned by
// projectID, ordered by created_at DESC. Used by the project-scoped
// /api/v1/projects/{name}/api-keys endpoint (D-1).
func (r *APIKeysRepo) ListByProject(ctx context.Context, projectID int64) ([]APIKey, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, owner_kind, owner_user_id, owner_project_id, name, token_prefix, token_sha256,
		       last_used_at, created_at, revoked_at
		FROM api_keys
		WHERE owner_kind='project' AND owner_project_id=? AND revoked_at IS NULL
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("api_keys: list by project %d: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var userID, projectID2 sql.NullInt64
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&k.ID, &k.OwnerKind, &userID, &projectID2, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
			&lastUsed, &k.CreatedAt, &revoked); err != nil {
			return nil, fmt.Errorf("api_keys: list scan: %w", err)
		}
		if userID.Valid {
			v := userID.Int64
			k.OwnerUserID = &v
		}
		if projectID2.Valid {
			v := projectID2.Int64
			k.OwnerProjectID = &v
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			k.LastUsedAt = &t
		}
		if revoked.Valid {
			t := revoked.Time
			k.RevokedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// TouchLastUsed updates api_keys.last_used_at. Invoked on every successful
// middleware auth (KEY-08).
func (r *APIKeysRepo) TouchLastUsed(ctx context.Context, id int64, t time.Time) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`, t.UTC(), id)
		if err != nil {
			return fmt.Errorf("api_keys: touch last_used %d: %w", id, err)
		}
		return nil
	})
}

// Revoke stamps api_keys.revoked_at=CURRENT_TIMESTAMP.
func (r *APIKeysRepo) Revoke(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE api_keys SET revoked_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("api_keys: revoke %d: %w", id, err)
		}
		return nil
	})
}
