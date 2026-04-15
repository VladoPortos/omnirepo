package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Repo mirrors the repos table.
type Repo struct {
	ID              int64
	ProjectID       int64
	Type            string // rpm|deb|pypi|docker|helm|git|raw
	Name            string
	DescriptionMD   string
	AutoScan        bool
	BlockOnSeverity string // none|low|medium|high|critical
	PublicRead      bool
	SizeBytes       int64
	CreatedAt       time.Time
	DeletedAt       *time.Time
}

// ReposRepo owns CRUD on repos.
//
// The table DDL (001_initial.up.sql) enforces:
//   - CHECK(type IN ('rpm','deb','pypi','docker','helm','git','raw')) — REPO-02
//   - CHECK(block_on_severity IN ('none','low','medium','high','critical'))
//   - UNIQUE(project_id, type, name) — REPO-01
//
// The app layer (bootstrap validator V13, api handlers) validates the same
// set before INSERT so bad input surfaces a typed Go error rather than the
// raw CHECK-constraint string.
type ReposRepo struct{ db *DB }

// NewReposRepo constructs a repo bound to db.
func NewReposRepo(db *DB) *ReposRepo { return &ReposRepo{db: db} }

// Create inserts a repo row and returns the generated id. Nil/empty optional
// arguments fall back to schema defaults.
func (r *ReposRepo) Create(
	ctx context.Context,
	projectID int64,
	typ, name, descriptionMD string,
	autoScan *bool,
	blockOnSeverity *string,
	publicRead *bool,
) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		// Assemble an insert that lets schema defaults apply when the pointer is nil.
		as := int64(1)
		if autoScan != nil {
			as = boolInt(*autoScan)
		}
		bos := "none"
		if blockOnSeverity != nil && *blockOnSeverity != "" {
			bos = *blockOnSeverity
		}
		pr := int64(0)
		if publicRead != nil {
			pr = boolInt(*publicRead)
		}
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO repos(project_id, type, name, description_md, auto_scan, block_on_severity, public_read)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, projectID, typ, name, descriptionMD, as, bos, pr)
		if execErr != nil {
			return fmt.Errorf("repos: create (project=%d type=%s name=%s): %w", projectID, typ, name, execErr)
		}
		lid, lidErr := res.LastInsertId()
		if lidErr != nil {
			return fmt.Errorf("repos: last insert id: %w", lidErr)
		}
		id = lid
		return nil
	})
	return id, err
}

// FindByTriple returns the live repo with matching (projectID, type, name).
func (r *ReposRepo) FindByTriple(ctx context.Context, projectID int64, typ, name string) (*Repo, error) {
	return r.scanOne(ctx, `project_id=? AND type=? AND name=? AND deleted_at IS NULL`, projectID, typ, name)
}

// FindByID returns the live repo by id.
func (r *ReposRepo) FindByID(ctx context.Context, id int64) (*Repo, error) {
	return r.scanOne(ctx, `id=? AND deleted_at IS NULL`, id)
}

// SoftDelete stamps deleted_at.
func (r *ReposRepo) SoftDelete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("repos: soft delete %d: %w", id, err)
		}
		return nil
	})
}

// ListByProject returns every live repo belonging to projectID.
func (r *ReposRepo) ListByProject(ctx context.Context, projectID int64) ([]Repo, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at
		FROM repos WHERE project_id=? AND deleted_at IS NULL
		ORDER BY type, name
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("repos: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		rr, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rr)
	}
	return out, rows.Err()
}

// ListAll returns every live repo across every project.
func (r *ReposRepo) ListAll(ctx context.Context) ([]Repo, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at
		FROM repos WHERE deleted_at IS NULL ORDER BY project_id, type, name
	`)
	if err != nil {
		return nil, fmt.Errorf("repos: list all: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		rr, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rr)
	}
	return out, rows.Err()
}

func (r *ReposRepo) scanOne(ctx context.Context, where string, args ...any) (*Repo, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at
		FROM repos WHERE `+where, args...)
	rr, err := scanRepoRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repos: scan: %w", err)
	}
	return rr, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRepo(rs scanner) (*Repo, error) {
	return scanRepoRow(rs)
}

func scanRepoRow(rs scanner) (*Repo, error) {
	var r Repo
	var as, pr int64
	var deleted sql.NullTime
	if err := rs.Scan(&r.ID, &r.ProjectID, &r.Type, &r.Name, &r.DescriptionMD, &as, &r.BlockOnSeverity, &pr, &r.SizeBytes, &r.CreatedAt, &deleted); err != nil {
		return nil, err
	}
	r.AutoScan = as != 0
	r.PublicRead = pr != 0
	if deleted.Valid {
		t := deleted.Time
		r.DeletedAt = &t
	}
	return &r, nil
}
