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

// fmtTS is the canonical DBTimestampLayout formatter used by every write
// on the sessions table. F-04.3: modernc/sqlite serializes time.Time via
// Go's %v format (variable fractional-second width) which breaks lex
// comparison against CURRENT_TIMESTAMP at sub-second boundaries. Using
// the fixed-width layout for every write + passing the current-time as
// an explicit parameter to FindByPrefixSha (instead of CURRENT_TIMESTAMP)
// keeps the comparison monotonic.
func fmtTS(t time.Time) string {
	return t.UTC().Format(DBTimestampLayout)
}

// Create inserts a session row and returns the generated id.
func (r *SessionsRepo) Create(ctx context.Context, userID int64, prefix, sha256hex string, issuedAt, expiresAt time.Time) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		issued := fmtTS(issuedAt)
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO sessions(user_id, token_prefix, token_sha256, issued_at, last_seen_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, userID, prefix, sha256hex, issued, issued, fmtTS(expiresAt))
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
//
// F-04.3: comparing against CURRENT_TIMESTAMP would fail because stored
// values are now DBTimestampLayout ('T'-separated) while CURRENT_TIMESTAMP
// is space-separated — lex ordering diverges at position 10. Pass
// time.Now() formatted with DBTimestampLayout as an explicit bind instead.
func (r *SessionsRepo) FindByPrefixSha(ctx context.Context, prefix, sha256hex string) (*Session, error) {
	now := fmtTS(time.Now())
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, user_id, token_prefix, token_sha256, issued_at, last_seen_at, expires_at
		FROM sessions
		WHERE token_prefix=? AND token_sha256=? AND expires_at>?
	`, prefix, sha256hex, now)
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
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE id=?`, fmtTS(t), id)
		if err != nil {
			return fmt.Errorf("sessions: touch last seen %d: %w", id, err)
		}
		return nil
	})
}

// SlideExpiry extends sessions.expires_at (and bumps last_seen_at) to
// newExpires for session id. Callers compute newExpires as
// min(now + session_ttl, issued_at + hard_cap) — see D-07 (12h sliding,
// 7d hard cap). The UPDATE is a no-op if newExpires is not strictly later
// than the existing expires_at, so two concurrent touches cannot walk the
// cap backwards.
func (r *SessionsRepo) SlideExpiry(ctx context.Context, id int64, seenAt, newExpires time.Time) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		newExp := fmtTS(newExpires)
		_, err := tx.ExecContext(ctx, `
			UPDATE sessions
			SET last_seen_at=?, expires_at=?
			WHERE id=? AND expires_at < ?
		`, fmtTS(seenAt), newExp, id, newExp)
		if err != nil {
			return fmt.Errorf("sessions: slide expiry %d: %w", id, err)
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

// DeleteAllForUser removes every session belonging to userID. Called after
// admin password resets so stolen cookies do not survive the rotation.
func (r *SessionsRepo) DeleteAllForUser(ctx context.Context, userID int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
		if err != nil {
			return fmt.Errorf("sessions: delete all for user %d: %w", userID, err)
		}
		return nil
	})
}

// DeleteAllForUserExcept removes every session belonging to userID except
// exceptID. Used by self-service password change to keep the current browser
// session alive while invalidating every other session (stolen cookies,
// forgotten devices).
func (r *SessionsRepo) DeleteAllForUserExcept(ctx context.Context, userID, exceptID int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND id<>?`, userID, exceptID)
		if err != nil {
			return fmt.Errorf("sessions: delete all for user %d except %d: %w", userID, exceptID, err)
		}
		return nil
	})
}
