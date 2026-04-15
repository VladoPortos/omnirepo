package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DEBPackage mirrors one deb_packages row (Phase 03 Plan 01, D-26).
type DEBPackage struct {
	ID           int64
	RepoID       int64
	SuiteID      int64
	Package      string
	Version      string
	Architecture string
	Maintainer   string
	Section      string
	Priority     string
	Depends      string
	Description  string
	SizeBytes    int64
	Digest       string
	Filename     string
	UploadedAt   time.Time
}

// DEBPackagesRepo owns CRUD on deb_packages. suite_id comes from
// AptSuitesRepo.Insert inside the same writer tx.
type DEBPackagesRepo struct{ db *DB }

// NewDEBPackagesRepo constructs the repo bound to db.
func NewDEBPackagesRepo(db *DB) *DEBPackagesRepo { return &DEBPackagesRepo{db: db} }

// Insert upserts by (repo_id, suite_id, package, version, architecture).
// Returns the row id.
func (r *DEBPackagesRepo) Insert(ctx context.Context, tx *sql.Tx, p *DEBPackage) (int64, error) {
	if p == nil {
		return 0, errors.New("deb_packages: nil package")
	}
	if p.Package == "" || p.Version == "" || p.Architecture == "" || p.Digest == "" || p.Filename == "" {
		return 0, errors.New("deb_packages: package/version/architecture/digest/filename required")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deb_packages(
			repo_id, suite_id, package, version, architecture,
			maintainer, section, priority, depends, description,
			size_bytes, digest, filename
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, suite_id, package, version, architecture) DO UPDATE SET
			maintainer  = excluded.maintainer,
			section     = excluded.section,
			priority    = excluded.priority,
			depends     = excluded.depends,
			description = excluded.description,
			size_bytes  = excluded.size_bytes,
			digest      = excluded.digest,
			filename    = excluded.filename,
			uploaded_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, p.RepoID, p.SuiteID, p.Package, p.Version, p.Architecture,
		p.Maintainer, p.Section, p.Priority, p.Depends, p.Description,
		p.SizeBytes, p.Digest, p.Filename); err != nil {
		return 0, fmt.Errorf("deb_packages: upsert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM deb_packages
		WHERE repo_id=? AND suite_id=? AND package=? AND version=? AND architecture=?
	`, p.RepoID, p.SuiteID, p.Package, p.Version, p.Architecture).Scan(&id); err != nil {
		return 0, fmt.Errorf("deb_packages: read-back: %w", err)
	}
	return id, nil
}

// Delete removes the row by id.
func (r *DEBPackagesRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM deb_packages WHERE id=?`, id,
	); err != nil {
		return fmt.Errorf("deb_packages: delete %d: %w", id, err)
	}
	return nil
}

// FindByTuple returns the row matching (suite_id, package, version,
// architecture) scoped to repoID. Returns ErrNotFound on miss.
func (r *DEBPackagesRepo) FindByTuple(ctx context.Context, repoID, suiteID int64, pkg, version, arch string) (*DEBPackage, error) {
	return r.scanOne(ctx, `
		repo_id=? AND suite_id=? AND package=? AND version=? AND architecture=?
	`, repoID, suiteID, pkg, version, arch)
}

// FindByDigest returns the row matching digest inside repoID.
func (r *DEBPackagesRepo) FindByDigest(ctx context.Context, repoID int64, digest string) (*DEBPackage, error) {
	return r.scanOne(ctx, `repo_id=? AND digest=?`, repoID, digest)
}

// ListBySuite returns every row for the given suite_id, ordered by package
// then version descending (newest first).
func (r *DEBPackagesRepo) ListBySuite(ctx context.Context, suiteID int64) ([]DEBPackage, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, suite_id, package, version, architecture,
		       maintainer, section, priority, depends, description,
		       size_bytes, digest, filename, uploaded_at
		FROM deb_packages WHERE suite_id=?
		ORDER BY package, version DESC
	`, suiteID)
	if err != nil {
		return nil, fmt.Errorf("deb_packages: list by suite: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DEBPackage
	for rows.Next() {
		p, err := scanDEBPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListByRepo returns every row across all suites for repoID.
func (r *DEBPackagesRepo) ListByRepo(ctx context.Context, repoID int64) ([]DEBPackage, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, suite_id, package, version, architecture,
		       maintainer, section, priority, depends, description,
		       size_bytes, digest, filename, uploaded_at
		FROM deb_packages WHERE repo_id=?
		ORDER BY suite_id, package, version DESC
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("deb_packages: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DEBPackage
	for rows.Next() {
		p, err := scanDEBPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *DEBPackagesRepo) scanOne(ctx context.Context, where string, args ...any) (*DEBPackage, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, suite_id, package, version, architecture,
		       maintainer, section, priority, depends, description,
		       size_bytes, digest, filename, uploaded_at
		FROM deb_packages WHERE `+where, args...)
	p, err := scanDEBPackage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("deb_packages: scan: %w", err)
	}
	return p, nil
}

func scanDEBPackage(rs scanner) (*DEBPackage, error) {
	var p DEBPackage
	var uploaded string
	if err := rs.Scan(
		&p.ID, &p.RepoID, &p.SuiteID, &p.Package, &p.Version, &p.Architecture,
		&p.Maintainer, &p.Section, &p.Priority, &p.Depends, &p.Description,
		&p.SizeBytes, &p.Digest, &p.Filename, &uploaded,
	); err != nil {
		return nil, err
	}
	p.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
	return &p, nil
}
