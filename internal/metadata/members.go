package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrLastMaintainer is returned by RemoveGuarded and UpdateRoleGuarded when
// the operation would leave the project without any human maintainer. Callers
// (handleRemoveMember, handlePatchMember) should translate this to a 409
// codeRBACLastMaintainer response.
//
// Super-admin callers bypass the guard by calling Remove / UpdateRole directly
// — they can always rescue a zero-maintainer project.
var ErrLastMaintainer = errors.New("members: last maintainer")

// MembersRepo owns CRUD on project_members.
//
// The table is a (project_id, user_id, role) triple; there are no soft-delete
// semantics (removing a member actually DELETEs the row). Membership history
// is not tracked; the audit_log is the source of truth for
// member.added / member.removed / member.role_changed events.
//
// The role column was added by migration 034. Valid values are
// 'maintainer' (full write access) and 'viewer' (read-only). The DB CHECK
// constraint enforces the enum; the application layer validates and defaults
// before inserting (new inserts default to 'viewer'; creator auto-add uses
// 'maintainer').
type MembersRepo struct{ db *DB }

// NewMembersRepo constructs a repo bound to db.
func NewMembersRepo(db *DB) *MembersRepo { return &MembersRepo{db: db} }

// Add inserts a project_members row with the given role.
// Duplicate (project_id, user_id) pairs surface the PK-conflict error.
// role must be "maintainer" or "viewer"; the DB CHECK constraint rejects
// any other value (including "owner").
func (r *MembersRepo) Add(ctx context.Context, projectID, userID int64, role string) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.AddInTx(ctx, tx, projectID, userID, role)
	})
}

// AddInTx is the tx-scoped form of Add (same role param). It lets callers
// compose membership insertion with another mutation (e.g. the
// project insert in handleCreateProject) so either both rows commit together
// or the whole operation rolls back.
func (r *MembersRepo) AddInTx(ctx context.Context, tx *sql.Tx, projectID, userID int64, role string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_members(project_id, user_id, role) VALUES (?, ?, ?)
	`, projectID, userID, role); err != nil {
		return fmt.Errorf("members: add (%d,%d): %w", projectID, userID, err)
	}
	return nil
}

// UpdateRole changes the role of an existing project member.
// Returns a "not found" error when no row matches (projectID, userID).
// Used by handlePatchMember; wraps the UPDATE in a WriteTx so concurrent
// demote requests serialize via SQLite's _txlock=immediate.
func (r *MembersRepo) UpdateRole(ctx context.Context, projectID, userID int64, role string) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE project_members SET role=? WHERE project_id=? AND user_id=?
		`, role, projectID, userID)
		if err != nil {
			return fmt.Errorf("members: update role (%d,%d): %w", projectID, userID, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("members: not found (%d,%d)", projectID, userID)
		}
		return nil
	})
}

// GetRole returns the role of (projectID, userID) and true when the row
// exists; returns ("", false) when the member is absent. Used by
// handlePatchMember (to capture old_role for the audit shape) and
// handleRemoveMember (to decide whether the target is a maintainer before
// the last-maintainer count check).
func (r *MembersRepo) GetRole(ctx context.Context, projectID, userID int64) (string, bool) {
	var role string
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT role FROM project_members WHERE project_id=? AND user_id=?
	`, projectID, userID).Scan(&role)
	if err != nil {
		// sql.ErrNoRows → not a member; any other DB error also collapses to
		// (not found) because callers use the bool to decide whether to
		// proceed. Errors are intentionally swallowed here; the caller's
		// !found branch will surface the right HTTP status code.
		return "", false
	}
	return role, true
}

// CountMaintainers returns the number of non-deleted human maintainers for
// projectID. Used by the last-maintainer guard in handlePatchMember and
// handleRemoveMember.
//
// The SQL joins users to exclude soft-deleted accounts; it does NOT
// count project-scoped API keys (they're not users) or super-admins (they
// bypass via actor.IsSuperAdmin and aren't in project_members).
func (r *MembersRepo) CountMaintainers(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = ? AND pm.role = 'maintainer' AND u.deleted_at IS NULL
	`, projectID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("members: count maintainers for project %d: %w", projectID, err)
	}
	return n, nil
}

// ListProjectRolesByNameForUser returns a map of projectName → role for every
// non-deleted project the user is a member of. Returns a non-nil empty map
// (not error) when the user has no memberships. Used by handleMe to populate
// the project_roles field without an N+1 lookup (the JOIN provides names
// directly). Policy enforcement uses ListProjectRolesForUser (id-keyed);
// serialisation uses this method.
func (r *MembersRepo) ListProjectRolesByNameForUser(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT p.name, pm.role
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id
		WHERE pm.user_id=? AND p.deleted_at IS NULL
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("members: list roles by name for user %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var name, role string
		if err := rows.Scan(&name, &role); err != nil {
			return nil, fmt.Errorf("members: scan: %w", err)
		}
		out[name] = role
	}
	return out, rows.Err()
}

// ListProjectRolesForUser returns a map of projectID → role for every
// non-deleted project the user is a member of. Returns a non-nil empty map
// (not error) when the user has no memberships. Used by ResolveMembership
// (auth middleware) and handleMe (/me project_roles field).
func (r *MembersRepo) ListProjectRolesForUser(ctx context.Context, userID int64) (map[int64]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT pm.project_id, pm.role
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id
		WHERE pm.user_id=? AND p.deleted_at IS NULL
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("members: list roles for user %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]string)
	for rows.Next() {
		var id int64
		var role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, fmt.Errorf("members: scan: %w", err)
		}
		out[id] = role
	}
	return out, rows.Err()
}

// Remove deletes the row; no error if the row is absent.
//
// Non-super-admin callers enforcing the last-maintainer guard MUST use
// RemoveGuarded instead — Remove does not check the guard and would let a
// concurrent pair of deletes race to zero maintainers.
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

// RemoveGuarded deletes (projectID, userID) atomically with the
// last-maintainer guard. The read-role, count-maintainers, and DELETE
// all run inside a single WriteTx, so two concurrent removes cannot both pass
// the count==2 guard and drop the project to zero maintainers.
//
// Returns ErrLastMaintainer when removing the row would leave zero human
// maintainers. Returns nil (idempotent) when the row is absent. Super-admin
// callers should use Remove directly — they bypass the guard.
func (r *MembersRepo) RemoveGuarded(ctx context.Context, projectID, userID int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var currentRole string
		err := tx.QueryRowContext(ctx, `
			SELECT role FROM project_members WHERE project_id=? AND user_id=?
		`, projectID, userID).Scan(&currentRole)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // Remove is idempotent — absent rows are a no-op.
		}
		if err != nil {
			return fmt.Errorf("members: remove-guarded read (%d,%d): %w", projectID, userID, err)
		}
		if currentRole == "maintainer" {
			var n int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM project_members pm
				JOIN users u ON u.id = pm.user_id
				WHERE pm.project_id = ? AND pm.role = 'maintainer' AND u.deleted_at IS NULL
			`, projectID).Scan(&n); err != nil {
				return fmt.Errorf("members: remove-guarded count (%d): %w", projectID, err)
			}
			if n <= 1 {
				return ErrLastMaintainer
			}
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM project_members WHERE project_id=? AND user_id=?
		`, projectID, userID); err != nil {
			return fmt.Errorf("members: remove-guarded delete (%d,%d): %w", projectID, userID, err)
		}
		return nil
	})
}

// UpdateRoleGuarded sets (projectID, userID) to newRole atomically with the
// last-maintainer guard. Returns the prior role (for audit emission)
// and ErrLastMaintainer when the change would drop the project to
// zero maintainers (demoting the final maintainer to viewer).
//
// Returns sql.ErrNoRows when the member does not exist.
//
// Super-admin callers should use UpdateRole directly — they bypass the guard.
func (r *MembersRepo) UpdateRoleGuarded(ctx context.Context, projectID, userID int64, newRole string) (oldRole string, err error) {
	err = r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT role FROM project_members WHERE project_id=? AND user_id=?
		`, projectID, userID).Scan(&oldRole); err != nil {
			return err // sql.ErrNoRows propagates; caller returns 404.
		}
		// Only check the guard when demoting a current maintainer to viewer.
		// Promotions and no-op changes (maintainer→maintainer, viewer→viewer)
		// cannot reduce the count.
		if oldRole == "maintainer" && newRole == "viewer" {
			var n int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM project_members pm
				JOIN users u ON u.id = pm.user_id
				WHERE pm.project_id = ? AND pm.role = 'maintainer' AND u.deleted_at IS NULL
			`, projectID).Scan(&n); err != nil {
				return fmt.Errorf("members: update-guarded count (%d): %w", projectID, err)
			}
			if n <= 1 {
				return ErrLastMaintainer
			}
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE project_members SET role=? WHERE project_id=? AND user_id=?
		`, newRole, projectID, userID)
		if err != nil {
			return fmt.Errorf("members: update-guarded (%d,%d): %w", projectID, userID, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
	return oldRole, err
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
	// Exclude soft-deleted projects so visibility helpers (dashboard, search,
	// projects list, admin user-detail rollup) never surface a project a user
	// can no longer reach. Soft-delete is an UPDATE, so project_members rows
	// survive until the project is hard-purged — without this filter we'd
	// hand out IDs that error at every downstream join.
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT pm.project_id
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id
		WHERE pm.user_id=? AND p.deleted_at IS NULL
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
