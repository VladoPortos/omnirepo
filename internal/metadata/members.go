package metadata

import (
	"context"
	"database/sql"
	"fmt"
)

// MembersRepo owns CRUD on project_members.
//
// The table is a pure (project_id, user_id) pair with an added_at timestamp;
// there are no soft-delete semantics (removing a member actually DELETEs the
// row). Phase 1 does not track membership history; the audit_log is the
// source of truth for member.added / member.removed events.
type MembersRepo struct{ db *DB }

// NewMembersRepo constructs a repo bound to db.
func NewMembersRepo(db *DB) *MembersRepo { return &MembersRepo{db: db} }

// Add inserts a project_members row. Duplicate (project_id, user_id) pairs
// surface the PK-conflict error from SQLite.
func (r *MembersRepo) Add(ctx context.Context, projectID, userID int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO project_members(project_id, user_id) VALUES (?, ?)
		`, projectID, userID)
		if err != nil {
			return fmt.Errorf("members: add (%d,%d): %w", projectID, userID, err)
		}
		return nil
	})
}

// Remove deletes the row; no error if the row is absent.
func (r *MembersRepo) Remove(ctx context.Context, projectID, userID int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM project_members WHERE project_id=? AND user_id=?
		`, projectID, userID)
		if err != nil {
			return fmt.Errorf("members: remove (%d,%d): %w", projectID, userID, err)
		}
		return nil
	})
}

// IsMember returns true if (projectID, userID) exists.
func (r *MembersRepo) IsMember(ctx context.Context, projectID, userID int64) (bool, error) {
	var n int
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM project_members WHERE project_id=? AND user_id=?
	`, projectID, userID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("members: is_member: %w", err)
	}
	return n > 0, nil
}

// ListProjectIDsForUser returns every project id userID is a member of. Empty
// slice (not error) when the user has no memberships.
func (r *MembersRepo) ListProjectIDsForUser(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT project_id FROM project_members WHERE user_id=?
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("members: list for user %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("members: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListUserIDsInProject returns every user id that belongs to projectID.
func (r *MembersRepo) ListUserIDsInProject(ctx context.Context, projectID int64) ([]int64, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT user_id FROM project_members WHERE project_id=?
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("members: list for project %d: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("members: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
