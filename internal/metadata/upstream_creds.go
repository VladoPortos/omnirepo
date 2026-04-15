// Package metadata — UpstreamCredsRepo implements the project-scoped
// upstream-credentials store (D-09, D-11). Secrets (password, token) are
// encrypted at rest via internal/crypto AES-GCM-256; the repo is the only
// code path that decrypts — and only via Lookup, which is reserved for
// pull-external (Phase 02-10).
//
// List/Get never select *_enc columns and never return plaintext. This is
// enforced structurally: List and Get SELECT only the secret-free
// projection, so the CredMeta struct itself cannot carry a secret.
package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
)

// ErrSecretRequired is returned when Create/Update are called with both
// password and token empty. At least one secret is mandatory per D-11.
var ErrSecretRequired = errors.New("upstream_creds: password or token required")

// ErrForeignProject is returned by Lookup/Get/Update/Delete when the cred id
// exists but belongs to a different project. Handlers should surface this
// as 404 (D-11 cross-project protection).
var ErrForeignProject = errors.New("upstream_creds: belongs to a different project")

// CredKind is the compile-time enum for upstream_creds.kind.
type CredKind string

// Known upstream credential kinds. The SQL CHECK constraint enforces the
// same set at the DB layer.
const (
	CredKindDocker CredKind = "docker"
	CredKindRPM    CredKind = "rpm"
	CredKindAPT    CredKind = "apt"
	CredKindPyPI   CredKind = "pypi"
	CredKindHelm   CredKind = "helm"
)

// ValidCredKinds enumerates every accepted CredKind. Handlers use this to
// reject bad input before hitting the DB CHECK constraint.
var ValidCredKinds = map[CredKind]struct{}{
	CredKindDocker: {},
	CredKindRPM:    {},
	CredKindAPT:    {},
	CredKindPyPI:   {},
	CredKindHelm:   {},
}

// CredMeta is the secret-free view of an upstream_creds row. This is the
// ONLY struct that leaves the repo for List/Get — the fact that it has no
// Password/Token fields is the structural guarantee that callers can never
// accidentally echo a secret.
type CredMeta struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	Host      string    `json:"host"`
	Kind      CredKind  `json:"kind"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpstreamCredsRepo is the typed repo for the upstream_creds table. The
// AEAD is captured at construction; all encrypt/decrypt paths go through it.
type UpstreamCredsRepo struct {
	db   *DB
	aead *omrcrypto.AEAD
}

// NewUpstreamCredsRepo constructs a repo bound to db and the per-install
// master AEAD. The AEAD is materialized at app boot (app.BootEnsureAEADKey).
func NewUpstreamCredsRepo(db *DB, a *omrcrypto.AEAD) *UpstreamCredsRepo {
	return &UpstreamCredsRepo{db: db, aead: a}
}

// Create inserts a new upstream_cred row. At least one of password/token
// must be non-empty. createdByActorID may be zero (unauthenticated flow
// never hits this path, but zero is accepted as NULL for robustness).
func (r *UpstreamCredsRepo) Create(
	ctx context.Context,
	projectID int64,
	host string,
	kind CredKind,
	username, password, token string,
	createdByActorID int64,
) (int64, error) {
	if password == "" && token == "" {
		return 0, ErrSecretRequired
	}
	if _, ok := ValidCredKinds[kind]; !ok {
		return 0, fmt.Errorf("upstream_creds: invalid kind %q", kind)
	}

	var pwEnc, tokEnc string
	if password != "" {
		var err error
		pwEnc, err = r.aead.Encrypt([]byte(password))
		if err != nil {
			return 0, fmt.Errorf("upstream_creds: encrypt password: %w", err)
		}
	}
	if token != "" {
		var err error
		tokEnc, err = r.aead.Encrypt([]byte(token))
		if err != nil {
			return 0, fmt.Errorf("upstream_creds: encrypt token: %w", err)
		}
	}

	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		createdBy := sql.NullInt64{Int64: createdByActorID, Valid: createdByActorID != 0}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO upstream_creds
				(project_id, host, kind, username, password_enc, token_enc, created_by_actor_id)
			VALUES (?,?,?,?,?,?,?)
		`, projectID, host, string(kind), username, pwEnc, tokEnc, createdBy)
		if err != nil {
			return fmt.Errorf("upstream_creds: insert: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("upstream_creds: last id: %w", err)
		}
		return nil
	})
	return id, err
}

// List returns every upstream cred for projectID, secret-free. Ordered by
// created_at ascending for stable UI presentation.
func (r *UpstreamCredsRepo) List(ctx context.Context, projectID int64) ([]CredMeta, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, host, kind, username, created_at, updated_at
		FROM upstream_creds
		WHERE project_id=?
		ORDER BY created_at ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("upstream_creds: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []CredMeta
	for rows.Next() {
		var m CredMeta
		var kind string
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Host, &kind, &m.Username, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("upstream_creds: scan: %w", err)
		}
		m.Kind = CredKind(kind)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get returns a single upstream cred's metadata. Returns ErrNotFound when
// no such id exists; returns ErrForeignProject when the id exists but in a
// different project (so handlers can surface 404 without leaking existence).
func (r *UpstreamCredsRepo) Get(ctx context.Context, projectID, id int64) (*CredMeta, error) {
	var m CredMeta
	var kind string
	var owningProject int64
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, project_id, host, kind, username, created_at, updated_at
		FROM upstream_creds
		WHERE id=?
	`, id).Scan(&m.ID, &owningProject, &m.Host, &kind, &m.Username, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("upstream_creds: get: %w", err)
	}
	if owningProject != projectID {
		return nil, ErrForeignProject
	}
	m.ProjectID = owningProject
	m.Kind = CredKind(kind)
	return &m, nil
}

// Update replaces username and re-encrypts a fresh secret. Per D-11 the full
// secret must be resubmitted; if both password and token are empty, returns
// ErrSecretRequired. Returns ErrNotFound / ErrForeignProject on mismatch.
func (r *UpstreamCredsRepo) Update(
	ctx context.Context,
	projectID, id int64,
	username, password, token string,
) error {
	if password == "" && token == "" {
		return ErrSecretRequired
	}

	var pwEnc, tokEnc string
	if password != "" {
		v, err := r.aead.Encrypt([]byte(password))
		if err != nil {
			return fmt.Errorf("upstream_creds: encrypt password: %w", err)
		}
		pwEnc = v
	}
	if token != "" {
		v, err := r.aead.Encrypt([]byte(token))
		if err != nil {
			return fmt.Errorf("upstream_creds: encrypt token: %w", err)
		}
		tokEnc = v
	}

	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		// Check existence + ownership first so callers get the right error.
		var owner int64
		if err := tx.QueryRowContext(ctx,
			`SELECT project_id FROM upstream_creds WHERE id=?`, id).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("upstream_creds: update lookup: %w", err)
		}
		if owner != projectID {
			return ErrForeignProject
		}
		// Update only what the caller provided; empty password/token means
		// "don't touch that secret" — but we already rejected both-empty
		// above, so at least one will be non-empty here.
		_, err := tx.ExecContext(ctx, `
			UPDATE upstream_creds SET
				username     = ?,
				password_enc = CASE WHEN ? = '' THEN password_enc ELSE ? END,
				token_enc    = CASE WHEN ? = '' THEN token_enc    ELSE ? END,
				updated_at   = CURRENT_TIMESTAMP
			WHERE id=? AND project_id=?
		`, username, pwEnc, pwEnc, tokEnc, tokEnc, id, projectID)
		if err != nil {
			return fmt.Errorf("upstream_creds: update: %w", err)
		}
		return nil
	})
}

// Delete removes the row. Returns ErrNotFound / ErrForeignProject on mismatch.
func (r *UpstreamCredsRepo) Delete(ctx context.Context, projectID, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var owner int64
		if err := tx.QueryRowContext(ctx,
			`SELECT project_id FROM upstream_creds WHERE id=?`, id).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("upstream_creds: delete lookup: %w", err)
		}
		if owner != projectID {
			return ErrForeignProject
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM upstream_creds WHERE id=? AND project_id=?`, id, projectID); err != nil {
			return fmt.Errorf("upstream_creds: delete: %w", err)
		}
		return nil
	})
}

// Lookup is the ONLY method that returns plaintext secrets. It is consumed
// at pull-external time (Phase 02-10) — never by REST responses. Callers
// must treat the returned password/token as write-only: feed into the
// upstream HTTP client, zero-length after use.
func (r *UpstreamCredsRepo) Lookup(ctx context.Context, projectID, id int64) (username, password, token, host string, err error) {
	var pwEnc, tokEnc string
	var owner int64
	err = r.db.Reader.QueryRowContext(ctx, `
		SELECT project_id, username, password_enc, token_enc, host
		FROM upstream_creds
		WHERE id=?
	`, id).Scan(&owner, &username, &pwEnc, &tokEnc, &host)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", "", ErrNotFound
		}
		err = fmt.Errorf("upstream_creds: lookup: %w", err)
		return
	}
	if owner != projectID {
		return "", "", "", "", ErrForeignProject
	}
	if pwEnc != "" {
		pw, derr := r.aead.Decrypt(pwEnc)
		if derr != nil {
			return "", "", "", "", fmt.Errorf("upstream_creds: decrypt password: %w", derr)
		}
		password = string(pw)
	}
	if tokEnc != "" {
		tk, derr := r.aead.Decrypt(tokEnc)
		if derr != nil {
			return "", "", "", "", fmt.Errorf("upstream_creds: decrypt token: %w", derr)
		}
		token = string(tk)
	}
	return
}
