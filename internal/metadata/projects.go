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
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO projects(name, description_md) VALUES (?, ?)
		`, name, descriptionMD)
		if execErr != nil {
			return fmt.Errorf("projects: create %q: %w", name, execErr)
		}
		lid, lidErr := res.LastInsertId()
		if lidErr != nil {
			return fmt.Errorf("projects: last insert id: %w", lidErr)
		}
		id = lid
		return nil
	})
	return id, err
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
