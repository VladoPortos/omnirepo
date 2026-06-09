package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GoModule mirrors one go_modules row: a single hosted Go module version.
// ModulePath is the decoded module path (e.g. github.com/Azure/azure-sdk);
// the GOPROXY handler derives the escaped on-disk form when serving.
type GoModule struct {
	ID         int64
	RepoID     int64
	ModulePath string
	Version    string
	SizeBytes  int64
	Digest     string
	UploadedAt time.Time
}

// GoModulesRepo owns CRUD on go_modules.
type GoModulesRepo struct{ db *DB }

// NewGoModulesRepo constructs the repo bound to db.
func NewGoModulesRepo(db *DB) *GoModulesRepo { return &GoModulesRepo{db: db} }

// Insert upserts by (repo_id, module_path, version). Returns the row id.
func (r *GoModulesRepo) Insert(ctx context.Context, tx *sql.Tx, m *GoModule) (int64, error) {
	if m == nil {
		return 0, errors.New("go_modules: nil module")
	}
	if m.ModulePath == "" || m.Version == "" || m.Digest == "" {
		return 0, errors.New("go_modules: module_path/version/digest required")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO go_modules(repo_id, module_path, version, size_bytes, digest)
		VALUES (?,?,?,?,?)
		ON CONFLICT(repo_id, module_path, version) DO UPDATE SET
			size_bytes  = excluded.size_bytes,
			digest      = excluded.digest,
			uploaded_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, m.RepoID, m.ModulePath, m.Version, m.SizeBytes, m.Digest); err != nil {
		return 0, fmt.Errorf("go_modules: upsert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM go_modules WHERE repo_id=? AND module_path=? AND version=?`,
		m.RepoID, m.ModulePath, m.Version,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("go_modules: read-back: %w", err)
	}
	return id, nil
}

// Delete removes the row by id.
func (r *GoModulesRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM go_modules WHERE id=?`, id,
	); err != nil {
		return fmt.Errorf("go_modules: delete %d: %w", id, err)
	}
	return nil
}

// FindByModuleVersion returns the row matching (module_path, version)
// inside repoID, or ErrNotFound.
func (r *GoModulesRepo) FindByModuleVersion(ctx context.Context, repoID int64, modulePath, version string) (*GoModule, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, module_path, version, size_bytes, digest, uploaded_at
		FROM go_modules WHERE repo_id=? AND module_path=? AND version=?
	`, repoID, modulePath, version)
	m, err := scanGoModule(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("go_modules: scan: %w", err)
	}
	return m, nil
}

// ListVersions returns every stored version for (repoID, modulePath) in
// insertion order. The GOPROXY handler sorts semver-aware before serving.
func (r *GoModulesRepo) ListVersions(ctx context.Context, repoID int64, modulePath string) ([]GoModule, error) {
	return r.list(ctx, `repo_id=? AND module_path=?`, repoID, modulePath)
}

// ListByRepo returns every module-version row for repoID, ordered by
// module path then version (string order; UI/list display only).
func (r *GoModulesRepo) ListByRepo(ctx context.Context, repoID int64) ([]GoModule, error) {
	return r.list(ctx, `repo_id=?`, repoID)
}

func (r *GoModulesRepo) list(ctx context.Context, where string, args ...any) ([]GoModule, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, module_path, version, size_bytes, digest, uploaded_at
		FROM go_modules WHERE `+where+`
		ORDER BY module_path, version
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("go_modules: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []GoModule
	for rows.Next() {
		m, err := scanGoModule(rows)
		if err != nil {
			return nil, fmt.Errorf("go_modules: scan: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func scanGoModule(rs scanner) (*GoModule, error) {
	var m GoModule
	var uploaded string
	if err := rs.Scan(
		&m.ID, &m.RepoID, &m.ModulePath, &m.Version, &m.SizeBytes, &m.Digest, &uploaded,
	); err != nil {
		return nil, err
	}
	m.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
	return &m, nil
}
