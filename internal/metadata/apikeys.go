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
//
// Role is populated from the api_keys.role column (v1.5 Phase 2). For
// user-owned keys it is always nil (the key inherits role from the owner's
// current project_members rows resolved at request time). For project-owned
// keys it is "maintainer" or "viewer" — the role baked in at mint time
// (D-23 / D-25 / D-27). The middleware threads this into Actor.APIKeyRole so
// the policy engine can gate writes correctly for viewer-minted project
// tokens.
type APIKey struct {
	ID             int64
	OwnerKind      string // "user" | "project"
	OwnerUserID    *int64
	OwnerProjectID *int64
	Name           string
	TokenPrefix    string
	TokenSHA256    string
	Role           *string // nil for user-owned keys; "maintainer" | "viewer" for project-owned
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

// CreateUserKey inserts an api_keys row owned by userID (role stays NULL —
// user-owned keys inherit role from project_members at request time).
func (r *APIKeysRepo) CreateUserKey(ctx context.Context, userID int64, name, prefix, sha256hex string) (int64, error) {
	return r.create(ctx, "user", &userID, nil, name, prefix, sha256hex, nil)
}

// CreateProjectKey inserts an api_keys row owned by projectID with no role
// (legacy/backfill path — role column stays NULL for callers that don't care).
// New code should prefer CreateProjectKeyWithRole to set the role explicitly.
func (r *APIKeysRepo) CreateProjectKey(ctx context.Context, projectID int64, name, prefix, sha256hex string) (int64, error) {
	return r.create(ctx, "project", nil, &projectID, name, prefix, sha256hex, nil)
}

// CreateProjectKeyWithRole inserts a project-owned api_keys row with an
// explicit role ("maintainer" or "viewer"). Called by MintProjectAPIKey so
// project-scoped tokens carry a minted role (D-23 / D-25).
func (r *APIKeysRepo) CreateProjectKeyWithRole(ctx context.Context, projectID int64, name, prefix, sha256hex, role string) (int64, error) {
	return r.create(ctx, "project", nil, &projectID, name, prefix, sha256hex, &role)
}

func (r *APIKeysRepo) create(ctx context.Context, kind string, userID, projectID *int64, name, prefix, sha256hex string, role *string) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO api_keys(owner_kind, owner_user_id, owner_project_id, name, token_prefix, token_sha256, role)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, kind, userID, projectID, name, prefix, sha256hex, role)
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
		       role, last_used_at, created_at, revoked_at
		FROM api_keys
		WHERE token_prefix=? AND token_sha256=? AND revoked_at IS NULL
	`, prefix, sha256hex)
	var k APIKey
	var userID, projectID sql.NullInt64
	var role sql.NullString
	var lastUsed, revoked sql.NullTime
	if err := row.Scan(&k.ID, &k.OwnerKind, &userID, &projectID, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
		&role, &lastUsed, &k.CreatedAt, &revoked); err != nil {
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
	if role.Valid {
		v := role.String
		k.Role = &v
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
		       role, last_used_at, created_at, revoked_at
		FROM api_keys
		WHERE id=? AND revoked_at IS NULL
	`, id)
	var k APIKey
	var userID, projectID sql.NullInt64
	var role sql.NullString
	var lastUsed, revoked sql.NullTime
	if err := row.Scan(&k.ID, &k.OwnerKind, &userID, &projectID, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
		&role, &lastUsed, &k.CreatedAt, &revoked); err != nil {
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
	if role.Valid {
		v := role.String
		k.Role = &v
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
		       role, last_used_at, created_at, revoked_at
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
		var role sql.NullString
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&k.ID, &k.OwnerKind, &userID2, &projectID, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
			&role, &lastUsed, &k.CreatedAt, &revoked); err != nil {
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
		if role.Valid {
			v := role.String
			k.Role = &v
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
		       role, last_used_at, created_at, revoked_at
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
		var role sql.NullString
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&k.ID, &k.OwnerKind, &userID, &projectID2, &k.Name, &k.TokenPrefix, &k.TokenSHA256,
			&role, &lastUsed, &k.CreatedAt, &revoked); err != nil {
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
		if role.Valid {
			v := role.String
			k.Role = &v
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
//
// F-04.3: write via DBTimestampLayout (fixed-width ISO-8601) instead of
// raw time.Time binding — modernc/sqlite otherwise serializes as Go-%v
// with variable fractional-second width, breaking lex ordering.
func (r *APIKeysRepo) TouchLastUsed(ctx context.Context, id int64, t time.Time) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`, t.UTC().Format(DBTimestampLayout), id)
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

// RevokeAllByUser marks every live user-owned API key as revoked. Called on
// account deletion (F-03.6 wt3): users are soft-deleted, so the FK cascade
// that would normally clean up api_keys never fires — without this the
// rows stay around as DB garbage and the partial unique index on
// (owner_user_id, name) keeps claiming slot names for logins that no
// longer exist.
func (r *APIKeysRepo) RevokeAllByUser(ctx context.Context, userID int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api_keys
			SET revoked_at=CURRENT_TIMESTAMP
			WHERE owner_user_id=? AND revoked_at IS NULL
		`, userID)
		if err != nil {
			return fmt.Errorf("api_keys: revoke all by user %d: %w", userID, err)
		}
		return nil
	})
}
