package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Project mirrors the projects table.
type Project struct {
	ID            int64
	Name          string
	DescriptionMD string
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

// ProjectsRepo owns CRUD on projects.
type ProjectsRepo struct{ db *DB }

// NewProjectsRepo constructs a repo bound to db.
func NewProjectsRepo(db *DB) *ProjectsRepo { return &ProjectsRepo{db: db} }

// Create inserts a project row and returns the generated id. Duplicate names
// surface the UNIQUE-constraint error from SQLite verbatim.
func (r *ProjectsRepo) Create(ctx context.Context, name, descriptionMD string) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		id, insErr = r.CreateInTx(ctx, tx, name, descriptionMD)
		return insErr
	})
	return id, err
}

// CreateInTx is the tx-scoped form of Create. Audit finding #7: callers
// that need to compose the project insert with a follow-up mutation (e.g.
// adding the creator as a member) can do so in a single writer tx so
// either both rows commit or neither does, eliminating orphan projects.
func (r *ProjectsRepo) CreateInTx(ctx context.Context, tx *sql.Tx, name, descriptionMD string) (int64, error) {
	res, execErr := tx.ExecContext(ctx, `
		INSERT INTO projects(name, description_md) VALUES (?, ?)
	`, name, descriptionMD)
	if execErr != nil {
		return 0, fmt.Errorf("projects: create %q: %w", name, execErr)
	}
	lid, lidErr := res.LastInsertId()
	if lidErr != nil {
		return 0, fmt.Errorf("projects: last insert id: %w", lidErr)
	}
	return lid, nil
}

// FindByName returns the live project with matching name. Returns ErrNotFound
// when absent or soft-deleted.
func (r *ProjectsRepo) FindByName(ctx context.Context, name string) (*Project, error) {
	return r.scanOne(ctx, `name=? AND deleted_at IS NULL`, name)
}

// FindByID returns the live project by id.
func (r *ProjectsRepo) FindByID(ctx context.Context, id int64) (*Project, error) {
	return r.scanOne(ctx, `id=? AND deleted_at IS NULL`, id)
}

// SoftDelete stamps deleted_at for id. Idempotent.
func (r *ProjectsRepo) SoftDelete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE projects SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("projects: soft delete %d: %w", id, err)
		}
		return nil
	})
}

// Restore clears deleted_at for id. Idempotent for live rows.
func (r *ProjectsRepo) Restore(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE projects SET deleted_at=NULL WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("projects: restore %d: %w", id, err)
		}
		return nil
	})
}

// HardDelete removes the row permanently. Callers are responsible for
// cascading FK cleanup (repos, members, audit_log references remain
// intact via ON DELETE SET NULL on audit_log.actor_user_id only; other
// FKs rely on upstream callers ensuring no orphan children).
func (r *ProjectsRepo) HardDelete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM project_members WHERE project_id=?`, id); err != nil {
			return fmt.Errorf("projects: hard delete members %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id); err != nil {
			return fmt.Errorf("projects: hard delete %d: %w", id, err)
		}
		return nil
	})
}

// ListDeleted returns every soft-deleted project, newest first. Powers
// the admin Trash page project-restore flow.
func (r *ProjectsRepo) ListDeleted(ctx context.Context) ([]Project, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, name, description_md, created_at, deleted_at
		FROM projects WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("projects: list deleted: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		var p Project
		var deleted sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.DescriptionMD, &p.CreatedAt, &deleted); err != nil {
			return nil, fmt.Errorf("projects: scan: %w", err)
		}
		if deleted.Valid {
			t := deleted.Time
			p.DeletedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListAll returns every live project, ordered by name.
func (r *ProjectsRepo) ListAll(ctx context.Context) ([]Project, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, name, description_md, created_at, deleted_at
		FROM projects WHERE deleted_at IS NULL ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("projects: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		var p Project
		var deleted sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.DescriptionMD, &p.CreatedAt, &deleted); err != nil {
			return nil, fmt.Errorf("projects: scan: %w", err)
		}
		if deleted.Valid {
			t := deleted.Time
			p.DeletedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *ProjectsRepo) scanOne(ctx context.Context, where string, args ...any) (*Project, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, name, description_md, created_at, deleted_at
		FROM projects WHERE `+where, args...)
	var p Project
	var deleted sql.NullTime
	if err := row.Scan(&p.ID, &p.Name, &p.DescriptionMD, &p.CreatedAt, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("projects: scan: %w", err)
	}
	if deleted.Valid {
		t := deleted.Time
		p.DeletedAt = &t
	}
	return &p, nil
}
