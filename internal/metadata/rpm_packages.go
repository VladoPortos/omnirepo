package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RPMPackage mirrors one rpm_packages row.
type RPMPackage struct {
	ID          int64
	RepoID      int64
	Name        string
	Epoch       int
	Version     string
	Release     string
	Arch        string
	Summary     string
	Description string
	License     string
	URL         string
	SourceRPM   string
	SizeBytes   int64
	Digest      string
	Filename    string
	UploadedAt  time.Time
	// FilesJSON is the JSON-encoded per-package file index (the rpm protocol's
	// []File) used to rebuild filelists.xml on regen. Empty string is
	// normalized to "[]" on insert.
	FilesJSON string
}

// RPMPackagesRepo owns CRUD on rpm_packages. Writers ride in the caller's
// *sql.Tx so the INSERT + FTS5 IndexRPM + repos.metadata_state transition
// land atomically.
type RPMPackagesRepo struct{ db *DB }

// NewRPMPackagesRepo constructs the repo bound to db.
func NewRPMPackagesRepo(db *DB) *RPMPackagesRepo { return &RPMPackagesRepo{db: db} }

// Insert upserts by (repo_id, name, epoch, version, release, arch) —
// re-uploading the same NEVRA refreshes size/digest/filename but keeps
// the row id stable. Returns the row id.
func (r *RPMPackagesRepo) Insert(ctx context.Context, tx *sql.Tx, p *RPMPackage) (int64, error) {
	if p == nil {
		return 0, errors.New("rpm_packages: nil package")
	}
	if p.Name == "" || p.Version == "" || p.Release == "" || p.Arch == "" || p.Digest == "" || p.Filename == "" {
		return 0, errors.New("rpm_packages: name/version/release/arch/digest/filename required")
	}
	filesJSON := p.FilesJSON
	if filesJSON == "" {
		filesJSON = "[]"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rpm_packages(
			repo_id, name, epoch, version, release, arch,
			summary, description, license, url, source_rpm,
			size_bytes, digest, filename, files_json
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, name, epoch, version, release, arch) DO UPDATE SET
			summary     = excluded.summary,
			description = excluded.description,
			license     = excluded.license,
			url         = excluded.url,
			source_rpm  = excluded.source_rpm,
			size_bytes  = excluded.size_bytes,
			digest      = excluded.digest,
			filename    = excluded.filename,
			files_json  = excluded.files_json,
			uploaded_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, p.RepoID, p.Name, p.Epoch, p.Version, p.Release, p.Arch,
		p.Summary, p.Description, p.License, p.URL, p.SourceRPM,
		p.SizeBytes, p.Digest, p.Filename, filesJSON); err != nil {
		return 0, fmt.Errorf("rpm_packages: upsert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM rpm_packages
		WHERE repo_id=? AND name=? AND epoch=? AND version=? AND release=? AND arch=?
	`, p.RepoID, p.Name, p.Epoch, p.Version, p.Release, p.Arch).Scan(&id); err != nil {
		return 0, fmt.Errorf("rpm_packages: read-back: %w", err)
	}
	return id, nil
}

// Delete removes the row by id.
func (r *RPMPackagesRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM rpm_packages WHERE id=?`, id,
	); err != nil {
		return fmt.Errorf("rpm_packages: delete %d: %w", id, err)
	}
	return nil
}

// FindByNEVRA returns the row matching the NEVRA inside repoID. Returns
// ErrNotFound on miss.
func (r *RPMPackagesRepo) FindByNEVRA(ctx context.Context, repoID int64, name string, epoch int, version, release, arch string) (*RPMPackage, error) {
	return r.scanOne(ctx, `
		repo_id=? AND name=? AND epoch=? AND version=? AND release=? AND arch=?
	`, repoID, name, epoch, version, release, arch)
}

// FindByDigest returns the row matching digest inside repoID. Returns
// ErrNotFound on miss. Useful for deletion-by-digest in the RPM handler.
func (r *RPMPackagesRepo) FindByDigest(ctx context.Context, repoID int64, digest string) (*RPMPackage, error) {
	return r.scanOne(ctx, `repo_id=? AND digest=?`, repoID, digest)
}

// ListByRepo returns every row for repoID. Used by the regen goroutine
// to materialize primary.xml.gz; ordered by (name, arch, epoch DESC,
// version DESC, release DESC) so "latest first" shows up correctly.
func (r *RPMPackagesRepo) ListByRepo(ctx context.Context, repoID int64) ([]RPMPackage, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, name, epoch, version, release, arch,
		       summary, description, license, url, source_rpm,
		       size_bytes, digest, filename, uploaded_at, files_json
		FROM rpm_packages WHERE repo_id=?
		ORDER BY name, arch, epoch DESC, version DESC, release DESC
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("rpm_packages: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RPMPackage
	for rows.Next() {
		p, err := scanRPMPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *RPMPackagesRepo) scanOne(ctx context.Context, where string, args ...any) (*RPMPackage, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, name, epoch, version, release, arch,
		       summary, description, license, url, source_rpm,
		       size_bytes, digest, filename, uploaded_at, files_json
		FROM rpm_packages WHERE `+where, args...)
	p, err := scanRPMPackage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("rpm_packages: scan: %w", err)
	}
	return p, nil
}

func scanRPMPackage(rs scanner) (*RPMPackage, error) {
	var p RPMPackage
	var uploaded string
	if err := rs.Scan(
		&p.ID, &p.RepoID, &p.Name, &p.Epoch, &p.Version, &p.Release, &p.Arch,
		&p.Summary, &p.Description, &p.License, &p.URL, &p.SourceRPM,
		&p.SizeBytes, &p.Digest, &p.Filename, &uploaded, &p.FilesJSON,
	); err != nil {
		return nil, err
	}
	p.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
	return &p, nil
}
