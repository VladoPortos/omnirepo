package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// NPMPackage mirrors one npm_packages row: a published (name, version).
// VersionJSON is the version manifest exactly as published (minus
// _attachments); the packument endpoint reassembles the document from
// these rows.
type NPMPackage struct {
	ID          int64
	RepoID      int64
	Name        string
	Version     string
	Description string
	VersionJSON string
	Tarball     string
	SizeBytes   int64
	Shasum      string
	Integrity   string
	UploadedAt  time.Time
}

// NPMPackagesRepo owns CRUD on npm_packages + npm_dist_tags.
type NPMPackagesRepo struct{ db *DB }

// NewNPMPackagesRepo constructs the repo bound to db.
func NewNPMPackagesRepo(db *DB) *NPMPackagesRepo { return &NPMPackagesRepo{db: db} }

// Insert adds a (name, version) row. npm semantics are immutable: a
// duplicate (repo, name, version) is a constraint violation the handler
// maps to 403 (matching the public registry's "cannot publish over").
func (r *NPMPackagesRepo) Insert(ctx context.Context, tx *sql.Tx, p *NPMPackage) (int64, error) {
	if p == nil {
		return 0, errors.New("npm_packages: nil package")
	}
	if p.Name == "" || p.Version == "" || p.Tarball == "" || p.VersionJSON == "" {
		return 0, errors.New("npm_packages: name/version/tarball/version_json required")
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO npm_packages(
			repo_id, name, version, description, version_json,
			tarball, size_bytes, shasum, integrity
		) VALUES (?,?,?,?,?,?,?,?,?)
	`, p.RepoID, p.Name, p.Version, p.Description, p.VersionJSON,
		p.Tarball, p.SizeBytes, p.Shasum, p.Integrity)
	if err != nil {
		return 0, fmt.Errorf("npm_packages: insert: %w", err)
	}
	return res.LastInsertId()
}

// Delete removes the row by id.
func (r *NPMPackagesRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM npm_packages WHERE id=?`, id); err != nil {
		return fmt.Errorf("npm_packages: delete %d: %w", id, err)
	}
	return nil
}

// FindByNameVersion returns the row matching (name, version) inside
// repoID, or ErrNotFound.
func (r *NPMPackagesRepo) FindByNameVersion(ctx context.Context, repoID int64, name, version string) (*NPMPackage, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, name, version, description, version_json,
		       tarball, size_bytes, shasum, integrity, uploaded_at
		FROM npm_packages WHERE repo_id=? AND name=? AND version=?
	`, repoID, name, version)
	p, err := scanNPMPackage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("npm_packages: scan: %w", err)
	}
	return p, nil
}

// ListVersions returns every published version row for (repoID, name).
func (r *NPMPackagesRepo) ListVersions(ctx context.Context, repoID int64, name string) ([]NPMPackage, error) {
	return r.list(ctx, `repo_id=? AND name=?`, repoID, name)
}

// ListByRepo returns every package-version row for repoID.
func (r *NPMPackagesRepo) ListByRepo(ctx context.Context, repoID int64) ([]NPMPackage, error) {
	return r.list(ctx, `repo_id=?`, repoID)
}

func (r *NPMPackagesRepo) list(ctx context.Context, where string, args ...any) ([]NPMPackage, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, name, version, description, version_json,
		       tarball, size_bytes, shasum, integrity, uploaded_at
		FROM npm_packages WHERE `+where+`
		ORDER BY name, version
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("npm_packages: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []NPMPackage
	for rows.Next() {
		p, err := scanNPMPackage(rows)
		if err != nil {
			return nil, fmt.Errorf("npm_packages: scan: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// SetDistTag upserts a (name, tag) → version mapping.
func (r *NPMPackagesRepo) SetDistTag(ctx context.Context, tx *sql.Tx, repoID int64, name, tag, version string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO npm_dist_tags(repo_id, name, tag, version) VALUES (?,?,?,?)
		ON CONFLICT(repo_id, name, tag) DO UPDATE SET version=excluded.version
	`, repoID, name, tag, version); err != nil {
		return fmt.Errorf("npm_dist_tags: upsert: %w", err)
	}
	return nil
}

// DistTags returns the tag → version map for (repoID, name).
func (r *NPMPackagesRepo) DistTags(ctx context.Context, repoID int64, name string) (map[string]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx,
		`SELECT tag, version FROM npm_dist_tags WHERE repo_id=? AND name=?`, repoID, name)
	if err != nil {
		return nil, fmt.Errorf("npm_dist_tags: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var tag, version string
		if err := rows.Scan(&tag, &version); err != nil {
			return nil, fmt.Errorf("npm_dist_tags: scan: %w", err)
		}
		out[tag] = version
	}
	return out, rows.Err()
}

// DeleteDistTagsPointingAt removes any tag rows that resolve to version
// (used when the version row itself is deleted).
func (r *NPMPackagesRepo) DeleteDistTagsPointingAt(ctx context.Context, tx *sql.Tx, repoID int64, name, version string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM npm_dist_tags WHERE repo_id=? AND name=? AND version=?`,
		repoID, name, version); err != nil {
		return fmt.Errorf("npm_dist_tags: delete: %w", err)
	}
	return nil
}

func scanNPMPackage(rs scanner) (*NPMPackage, error) {
	var p NPMPackage
	var uploaded string
	if err := rs.Scan(
		&p.ID, &p.RepoID, &p.Name, &p.Version, &p.Description, &p.VersionJSON,
		&p.Tarball, &p.SizeBytes, &p.Shasum, &p.Integrity, &uploaded,
	); err != nil {
		return nil, err
	}
	p.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
	return &p, nil
}
