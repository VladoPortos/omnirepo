package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PyPIFile mirrors one pypi_files row (Phase 03 Plan 01, D-26). Kind is
// "wheel" or "sdist"; ProjectNormalized is the PEP-503 normalized name
// used as the Simple index grouping key.
type PyPIFile struct {
	ID                int64
	RepoID            int64
	ProjectNormalized string
	Version           string
	Filename          string
	Kind              string
	RequiresPython    string
	SizeBytes         int64
	Digest            string
	CoreMetadataJSON  string
	UploadedAt        time.Time
}

// PyPIFilesRepo owns CRUD on pypi_files. The Simple index regen walks
// rows by (repo_id, project_normalized) so ListByProject is hot path.
type PyPIFilesRepo struct{ db *DB }

// NewPyPIFilesRepo constructs the repo bound to db.
func NewPyPIFilesRepo(db *DB) *PyPIFilesRepo { return &PyPIFilesRepo{db: db} }

// Insert upserts by (repo_id, filename). Returns the row id.
func (r *PyPIFilesRepo) Insert(ctx context.Context, tx *sql.Tx, p *PyPIFile) (int64, error) {
	if p == nil {
		return 0, errors.New("pypi_files: nil file")
	}
	if p.Filename == "" || p.ProjectNormalized == "" || p.Version == "" || p.Digest == "" {
		return 0, errors.New("pypi_files: filename/project/version/digest required")
	}
	if p.Kind != "wheel" && p.Kind != "sdist" {
		return 0, fmt.Errorf("pypi_files: kind must be wheel|sdist, got %q", p.Kind)
	}
	coreMeta := p.CoreMetadataJSON
	if coreMeta == "" {
		coreMeta = "{}"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pypi_files(
			repo_id, project_normalized, version, filename, kind,
			requires_python, size_bytes, digest, core_metadata_json
		) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, filename) DO UPDATE SET
			project_normalized = excluded.project_normalized,
			version            = excluded.version,
			kind               = excluded.kind,
			requires_python    = excluded.requires_python,
			size_bytes         = excluded.size_bytes,
			digest             = excluded.digest,
			core_metadata_json = excluded.core_metadata_json,
			uploaded_at        = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, p.RepoID, p.ProjectNormalized, p.Version, p.Filename, p.Kind,
		p.RequiresPython, p.SizeBytes, p.Digest, coreMeta); err != nil {
		return 0, fmt.Errorf("pypi_files: upsert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM pypi_files WHERE repo_id=? AND filename=?`,
		p.RepoID, p.Filename,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("pypi_files: read-back: %w", err)
	}
	return id, nil
}

// Delete removes the row by id.
func (r *PyPIFilesRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pypi_files WHERE id=?`, id,
	); err != nil {
		return fmt.Errorf("pypi_files: delete %d: %w", id, err)
	}
	return nil
}

// FindByFilename returns the row matching filename inside repoID.
func (r *PyPIFilesRepo) FindByFilename(ctx context.Context, repoID int64, filename string) (*PyPIFile, error) {
	return r.scanOne(ctx, `repo_id=? AND filename=?`, repoID, filename)
}

// FindByFilenameTx is the in-transaction variant: reads through tx so it
// sees uncommitted writes made earlier in the same WriteTx. Returns
// (nil, nil) when no row exists so callers can check for duplicates
// without unwrapping ErrNotFound.
//
// Used by protocol/pypi.commitPyPIRow to enforce the PyPI immutability
// contract on twine-legacy + PEP 694 uploads (F-07.1, wt3 §7.7) without
// disturbing the mirror-sync idempotent-upsert path (which calls Insert
// directly).
func (r *PyPIFilesRepo) FindByFilenameTx(ctx context.Context, tx *sql.Tx, repoID int64, filename string) (*PyPIFile, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, repo_id, project_normalized, version, filename, kind,
		       requires_python, size_bytes, digest, core_metadata_json, uploaded_at
		FROM pypi_files WHERE repo_id=? AND filename=?
	`, repoID, filename)
	p, err := scanPyPIFile(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("pypi_files: find-by-filename-tx: %w", err)
	}
	return p, nil
}

// FindByDigest returns the row matching digest inside repoID.
func (r *PyPIFilesRepo) FindByDigest(ctx context.Context, repoID int64, digest string) (*PyPIFile, error) {
	return r.scanOne(ctx, `repo_id=? AND digest=?`, repoID, digest)
}

// ListByProject returns every file for a given PEP-503 normalized project
// name. Drives the Simple per-project index page regen.
func (r *PyPIFilesRepo) ListByProject(ctx context.Context, repoID int64, projectNormalized string) ([]PyPIFile, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, project_normalized, version, filename, kind,
		       requires_python, size_bytes, digest, core_metadata_json, uploaded_at
		FROM pypi_files WHERE repo_id=? AND project_normalized=?
		ORDER BY version DESC, filename
	`, repoID, projectNormalized)
	if err != nil {
		return nil, fmt.Errorf("pypi_files: list by project: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PyPIFile
	for rows.Next() {
		p, err := scanPyPIFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListProjects returns every distinct project_normalized for repoID.
// Drives the Simple root index regen.
func (r *PyPIFilesRepo) ListProjects(ctx context.Context, repoID int64) ([]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT DISTINCT project_normalized FROM pypi_files WHERE repo_id=?
		ORDER BY project_normalized
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("pypi_files: list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("pypi_files: scan project: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PyPIFilesRepo) scanOne(ctx context.Context, where string, args ...any) (*PyPIFile, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, project_normalized, version, filename, kind,
		       requires_python, size_bytes, digest, core_metadata_json, uploaded_at
		FROM pypi_files WHERE `+where, args...)
	p, err := scanPyPIFile(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("pypi_files: scan: %w", err)
	}
	return p, nil
}

func scanPyPIFile(rs scanner) (*PyPIFile, error) {
	var p PyPIFile
	var uploaded string
	if err := rs.Scan(
		&p.ID, &p.RepoID, &p.ProjectNormalized, &p.Version, &p.Filename, &p.Kind,
		&p.RequiresPython, &p.SizeBytes, &p.Digest, &p.CoreMetadataJSON, &uploaded,
	); err != nil {
		return nil, err
	}
	p.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
	return &p, nil
}
