package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned by repo lookups when the row is absent OR soft-deleted.
// Single sentinel shared across users/sessions/apikeys so callers can errors.Is
// without caring which repo they came from.
var ErrNotFound = errors.New("metadata: not found")

// User mirrors the users table shape (see migrations/001_initial.up.sql).
type User struct {
	ID                 int64
	Login              string
	Email              string
	AvatarSeed         string
	PasswordHash       string
	IsSuperAdmin       bool
	MustChangePassword bool
	PasswordChangedAt  *time.Time
	CreatedAt          time.Time
	DeletedAt          *time.Time
}

// UsersRepo owns CRUD on users. All writes go through DB.WriteTx so they
// serialize on the writer pool and share BEGIN IMMEDIATE semantics (see tx.go).
type UsersRepo struct{ db *DB }

// NewUsersRepo constructs a repo bound to db.
func NewUsersRepo(db *DB) *UsersRepo { return &UsersRepo{db: db} }

// Create inserts a user row and returns the generated id. Rejects duplicate
// logins via the UNIQUE constraint on users.login — the error wraps the sqlite
// constraint error verbatim.
func (r *UsersRepo) Create(ctx context.Context, login, email, passwordHash string, isSuperAdmin, mustChangePassword bool) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO users(login, email, password_hash, is_super_admin, must_change_password)
			VALUES (?, ?, ?, ?, ?)
		`, login, email, passwordHash, boolInt(isSuperAdmin), boolInt(mustChangePassword))
		if execErr != nil {
			return fmt.Errorf("users: create %q: %w", login, execErr)
		}
		lid, lidErr := res.LastInsertId()
		if lidErr != nil {
			return fmt.Errorf("users: last insert id: %w", lidErr)
		}
		id = lid
		return nil
	})
	return id, err
}

// FindByLogin returns the live (non-soft-deleted) user with matching login.
// Returns ErrNotFound when no row matches.
func (r *UsersRepo) FindByLogin(ctx context.Context, login string) (*User, error) {
	return r.scanOne(ctx, `login=? AND deleted_at IS NULL`, login)
}

// FindByID returns the live user with matching id. Returns ErrNotFound when
// no row matches or the row is soft-deleted.
func (r *UsersRepo) FindByID(ctx context.Context, id int64) (*User, error) {
	return r.scanOne(ctx, `id=? AND deleted_at IS NULL`, id)
}

// SetMustChangePassword flips users.must_change_password.
func (r *UsersRepo) SetMustChangePassword(ctx context.Context, id int64, v bool) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET must_change_password=? WHERE id=?`, boolInt(v), id)
		if err != nil {
			return fmt.Errorf("users: set must_change_password %d: %w", id, err)
		}
		return nil
	})
}

// UpdatePasswordHash sets the hash, stamps password_changed_at=CURRENT_TIMESTAMP,
// and clears must_change_password. Callers pass the already-hashed value.
func (r *UsersRepo) UpdatePasswordHash(ctx context.Context, id int64, newHash string) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE users
			SET password_hash=?, password_changed_at=CURRENT_TIMESTAMP, must_change_password=0
			WHERE id=?
		`, newHash, id)
		if err != nil {
			return fmt.Errorf("users: update password hash %d: %w", id, err)
		}
		return nil
	})
}

// SetIsSuperAdmin flips users.is_super_admin.
func (r *UsersRepo) SetIsSuperAdmin(ctx context.Context, id int64, v bool) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET is_super_admin=? WHERE id=?`, boolInt(v), id)
		if err != nil {
			return fmt.Errorf("users: set is_super_admin %d: %w", id, err)
		}
		return nil
	})
}

// Delete soft-deletes by stamping users.deleted_at=CURRENT_TIMESTAMP.
func (r *UsersRepo) Delete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("users: delete %d: %w", id, err)
		}
		return nil
	})
}

// scanOne runs a WHERE clause against the reader pool and decodes exactly one
// User. Returns ErrNotFound on sql.ErrNoRows.
func (r *UsersRepo) scanOne(ctx context.Context, where string, args ...any) (*User, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, login, email, avatar_seed, password_hash, is_super_admin,
		       must_change_password, password_changed_at, created_at, deleted_at
		FROM users WHERE `+where, args...)
	var u User
	var isSA, mcp int64
	var pwChanged sql.NullTime
	var deleted sql.NullTime
	if err := row.Scan(&u.ID, &u.Login, &u.Email, &u.AvatarSeed, &u.PasswordHash,
		&isSA, &mcp, &pwChanged, &u.CreatedAt, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("users: scan: %w", err)
	}
	u.IsSuperAdmin = isSA != 0
	u.MustChangePassword = mcp != 0
	if pwChanged.Valid {
		t := pwChanged.Time
		u.PasswordChangedAt = &t
	}
	if deleted.Valid {
		t := deleted.Time
		u.DeletedAt = &t
	}
	return &u, nil
}

// ListAll returns every live (non-soft-deleted) user ordered by login.
func (r *UsersRepo) ListAll(ctx context.Context) ([]User, error) {
	return r.listOrdered(ctx, false)
}

// ListAllIncludingDeleted returns every user including soft-deleted rows,
// ordered by login. Used by the admin "show deleted" toggle so operators
// can see users whose schema UNIQUE slot is held by a deleted_at IS NOT
// NULL row (F-7 admin half) — without this there was no path to find out
// why a previously-used login was still reserved.
func (r *UsersRepo) ListAllIncludingDeleted(ctx context.Context) ([]User, error) {
	return r.listOrdered(ctx, true)
}

func (r *UsersRepo) listOrdered(ctx context.Context, includeDeleted bool) ([]User, error) {
	q := `SELECT id, login, email, avatar_seed, password_hash, is_super_admin,
		       must_change_password, password_changed_at, created_at, deleted_at
		 FROM users`
	if !includeDeleted {
		q += ` WHERE deleted_at IS NULL`
	}
	q += ` ORDER BY login`
	rows, err := r.db.Reader.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("users: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		var u User
		var isSA, mcp int64
		var pwChanged sql.NullTime
		var deleted sql.NullTime
		if err := rows.Scan(&u.ID, &u.Login, &u.Email, &u.AvatarSeed, &u.PasswordHash,
			&isSA, &mcp, &pwChanged, &u.CreatedAt, &deleted); err != nil {
			return nil, fmt.Errorf("users: list scan: %w", err)
		}
		u.IsSuperAdmin = isSA != 0
		u.MustChangePassword = mcp != 0
		if pwChanged.Valid {
			t := pwChanged.Time
			u.PasswordChangedAt = &t
		}
		if deleted.Valid {
			t := deleted.Time
			u.DeletedAt = &t
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateEmail sets the user's email address.
func (r *UsersRepo) UpdateEmail(ctx context.Context, id int64, email string) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET email=? WHERE id=?`, email, id)
		if err != nil {
			return fmt.Errorf("users: update email %d: %w", id, err)
		}
		return nil
	})
}

// UpdateAvatarSeed sets the user's avatar seed string.
func (r *UsersRepo) UpdateAvatarSeed(ctx context.Context, id int64, seed string) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET avatar_seed=? WHERE id=?`, seed, id)
		if err != nil {
			return fmt.Errorf("users: update avatar_seed %d: %w", id, err)
		}
		return nil
	})
}

// UpdateProfile atomically sets email and/or avatar_seed in one tx (LO-01).
// Either pointer may be nil to leave that column untouched. Returns nil if
// both are nil.
func (r *UsersRepo) UpdateProfile(ctx context.Context, id int64, email, avatarSeed *string) error {
	if email == nil && avatarSeed == nil {
		return nil
	}
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		sets := []string{}
		args := []any{}
		if email != nil {
			sets = append(sets, "email=?")
			args = append(args, *email)
		}
		if avatarSeed != nil {
			sets = append(sets, "avatar_seed=?")
			args = append(args, *avatarSeed)
		}
		args = append(args, id)
		query := "UPDATE users SET " + strings.Join(sets, ", ") + " WHERE id=?"
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("users: update profile %d: %w", id, err)
		}
		return nil
	})
}

// Count returns the number of live (non-soft-deleted) users.
func (r *UsersRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`).Scan(&n)
	return n, err
}

// boolInt converts a Go bool to the sqlite 0/1 integer form used by our schema
// (which uses BOOLEAN but sqlite stores it as INTEGER anyway).
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
