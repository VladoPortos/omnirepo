package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Session mirrors one row of the sessions table.
type Session struct {
	ID          int64
	UserID      int64
	TokenPrefix string
	TokenSHA256 string
	IssuedAt    time.Time
	LastSeenAt  time.Time
	ExpiresAt   time.Time
}

// SessionsRepo owns CRUD on sessions. Sessions are looked up by (prefix, sha256)
// to keep the index on token_prefix narrow while still resisting
// prefix-collision attacks via the sha256 constant-time compare in the auth
// middleware.
type SessionsRepo struct{ db *DB }

// NewSessionsRepo constructs a repo bound to db.
func NewSessionsRepo(db *DB) *SessionsRepo { return &SessionsRepo{db: db} }

// Create inserts a session row and returns the generated id.
func (r *SessionsRepo) Create(ctx context.Context, userID int64, prefix, sha256hex string, issuedAt, expiresAt time.Time) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO sessions(user_id, token_prefix, token_sha256, issued_at, last_seen_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, userID, prefix, sha256hex, issuedAt.UTC(), issuedAt.UTC(), expiresAt.UTC())
		if execErr != nil {
			return fmt.Errorf("sessions: create user=%d: %w", userID, execErr)
		}
		lid, lidErr := res.LastInsertId()
		if lidErr != nil {
			return fmt.Errorf("sessions: last insert id: %w", lidErr)
		}
		id = lid
		return nil
	})
	return id, err
}

// FindByPrefixSha returns the live session whose (prefix, sha256) pair matches
// and which has not expired. Returns ErrNotFound on miss.
func (r *SessionsRepo) FindByPrefixSha(ctx context.Context, prefix, sha256hex string) (*Session, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, user_id, token_prefix, token_sha256, issued_at, last_seen_at, expires_at
		FROM sessions
		WHERE token_prefix=? AND token_sha256=? AND expires_at>CURRENT_TIMESTAMP
	`, prefix, sha256hex)
	var s Session
	if err := row.Scan(&s.ID, &s.UserID, &s.TokenPrefix, &s.TokenSHA256, &s.IssuedAt, &s.LastSeenAt, &s.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sessions: scan: %w", err)
	}
	return &s, nil
}

// TouchLastSeen updates sessions.last_seen_at for session id.
func (r *SessionsRepo) TouchLastSeen(ctx context.Context, id int64, t time.Time) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE id=?`, t.UTC(), id)
		if err != nil {
			return fmt.Errorf("sessions: touch last seen %d: %w", id, err)
		}
		return nil
	})
}

// Delete removes the session row.
func (r *SessionsRepo) Delete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("sessions: delete %d: %w", id, err)
		}
		return nil
	})
}
