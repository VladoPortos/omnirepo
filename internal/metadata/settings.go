package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SettingsRepo is a key/value-over-text store for small operational state
// in the settings table: seeded_from_bootstrap timestamp, bootstrap_sha256
// digest, docker_token_hmac_secret, etc. Values are opaque to this repo;
// callers handle encoding.
type SettingsRepo struct{ db *DB }

// NewSettingsRepo constructs a repo bound to db.
func NewSettingsRepo(db *DB) *SettingsRepo { return &SettingsRepo{db: db} }

// Get returns (value, nil) when the key exists, or ("", ErrNotFound) when
// absent. Errors from the driver surface as wrapped errors.
func (r *SettingsRepo) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.Reader.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("settings: get %q: %w", key, err)
	}
	return v, nil
}

// Set inserts or updates the key with value and bumps updated_at.
func (r *SettingsRepo) Set(ctx context.Context, key, value string) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO settings(key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP
		`, key, value)
		if err != nil {
			return fmt.Errorf("settings: set %q: %w", key, err)
		}
		return nil
	})
}

// SetTx is a transactional variant of Set. Used by bootstrap to land settings
// in the same writer tx that seeds users/projects/etc so atomicity holds
// across the whole seed.
func (r *SettingsRepo) SetTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP
	`, key, value)
	if err != nil {
		return fmt.Errorf("settings: set %q: %w", key, err)
	}
	return nil
}

// GetAll returns every (key, value) pair. Intended for admin tooling; not
// sorted.
func (r *SettingsRepo) GetAll(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("settings: get all: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("settings: scan: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}
