// Package metadata — SigningKeysRepo owns the per-repo OpenPGP signing
// keypair table. The private key is AES-GCM-encrypted at rest via
// internal/crypto AEAD; the struct returned by Lookup deliberately has no
// private bytes field so no accidental echo back to REST or logs is
// possible. Only LookupPrivate decrypts, and it is reserved for the regen
// goroutine.
package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
)

// SigningKeyMeta is the secret-free view of a signing_keys row. No
// PrivateEnc / PrivArmored field: the encrypted private material never
// leaves this package outside of LookupPrivate.
type SigningKeyMeta struct {
	ID            int64
	RepoID        int64
	Scope         string // always "repo" in v1
	KeyKind       string // always "gpg_rsa4096" in v1
	PublicArmored string
	Fingerprint   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// SigningKeysRepo is the typed repo for signing_keys. The AEAD is captured
// at construction; every decrypt path routes through it.
type SigningKeysRepo struct {
	db   *DB
	aead *omrcrypto.AEAD
}

// NewSigningKeysRepo constructs the repo bound to db and the per-install
// master AEAD (shared with upstream_creds — same settings row).
func NewSigningKeysRepo(db *DB, a *omrcrypto.AEAD) *SigningKeysRepo {
	return &SigningKeysRepo{db: db, aead: a}
}

// Insert encrypts privArmored via AEAD and writes the full row inside the
// caller's tx. Caller is expected to hold the writer tx already (typically
// the same tx that created the repo). Returns the new row id.
//
// Scope is hard-coded to "repo" and key_kind to "gpg_rsa4096" in v1.
func (r *SigningKeysRepo) Insert(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	publicArmored, privArmored, fingerprint string,
) (int64, error) {
	if publicArmored == "" || privArmored == "" || fingerprint == "" {
		return 0, errors.New("signing_keys: public/private/fingerprint required")
	}
	enc, err := r.aead.Encrypt([]byte(privArmored))
	if err != nil {
		return 0, fmt.Errorf("signing_keys: encrypt: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO signing_keys(repo_id, scope, key_kind, public_armored, private_enc, fingerprint)
		VALUES (?, 'repo', 'gpg_rsa4096', ?, ?, ?)
	`, repoID, publicArmored, []byte(enc), fingerprint)
	if err != nil {
		return 0, fmt.Errorf("signing_keys: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("signing_keys: last insert id: %w", err)
	}
	return id, nil
}

// Lookup returns the secret-free view of the repo's signing key. Returns
// ErrNotFound if no row exists.
func (r *SigningKeysRepo) Lookup(ctx context.Context, repoID int64) (*SigningKeyMeta, error) {
	var m SigningKeyMeta
	var created, updated string
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, scope, key_kind, public_armored, fingerprint, created_at, updated_at
		FROM signing_keys WHERE repo_id = ? AND scope = 'repo'
	`, repoID).Scan(&m.ID, &m.RepoID, &m.Scope, &m.KeyKind, &m.PublicArmored, &m.Fingerprint, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("signing_keys: lookup: %w", err)
	}
	m.CreatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", created)
	m.UpdatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", updated)
	return &m, nil
}

// LookupPrivate decrypts and returns the armored private key. This is the
// ONLY path that touches AEAD.Decrypt. Callers must treat the returned
// string as write-only: feed into pgpsign.ClearSign/DetachSign, then let
// it fall out of scope. Errors never include decrypted bytes.
//
// Reserved for the regen goroutine scope — do not call from request
// handlers or anything user-facing.
func (r *SigningKeysRepo) LookupPrivate(ctx context.Context, repoID int64) (string, error) {
	var enc []byte
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT private_enc FROM signing_keys WHERE repo_id = ? AND scope = 'repo'
	`, repoID).Scan(&enc)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("signing_keys: lookup private: %w", err)
	}
	plain, err := r.aead.Decrypt(string(enc))
	if err != nil {
		return "", fmt.Errorf("signing_keys: decrypt: %w", err)
	}
	return string(plain), nil
}

// Delete removes the row for repoID. Typically handled automatically via
// the repos FK CASCADE; exposed for test cleanup + admin key rotation.
func (r *SigningKeysRepo) Delete(ctx context.Context, tx *sql.Tx, repoID int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM signing_keys WHERE repo_id = ?`, repoID,
	); err != nil {
		return fmt.Errorf("signing_keys: delete: %w", err)
	}
	return nil
}
