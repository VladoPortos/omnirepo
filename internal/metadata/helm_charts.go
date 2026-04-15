package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// HelmChart mirrors one helm_charts row (Phase 03 Plan 01, D-26).
// keywords_json / maintainers_json carry canonical JSON strings so
// index.yaml regen can emit them without re-parsing the chart.
type HelmChart struct {
	ID              int64
	RepoID          int64
	Name            string
	Version         string
	AppVersion      string
	Description     string
	KeywordsJSON    string
	MaintainersJSON string
	SizeBytes       int64
	Digest          string
	Filename        string
	UploadedAt      time.Time
}

// HelmChartsRepo owns CRUD on helm_charts.
type HelmChartsRepo struct{ db *DB }

// NewHelmChartsRepo constructs the repo bound to db.
func NewHelmChartsRepo(db *DB) *HelmChartsRepo { return &HelmChartsRepo{db: db} }

// Insert upserts by (repo_id, name, version). Returns the row id.
func (r *HelmChartsRepo) Insert(ctx context.Context, tx *sql.Tx, c *HelmChart) (int64, error) {
	if c == nil {
		return 0, errors.New("helm_charts: nil chart")
	}
	if c.Name == "" || c.Version == "" || c.Digest == "" || c.Filename == "" {
		return 0, errors.New("helm_charts: name/version/digest/filename required")
	}
	kw := c.KeywordsJSON
	if kw == "" {
		kw = "[]"
	}
	mt := c.MaintainersJSON
	if mt == "" {
		mt = "[]"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO helm_charts(
			repo_id, name, version, app_version, description,
			keywords_json, maintainers_json, size_bytes, digest, filename
		) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, name, version) DO UPDATE SET
			app_version      = excluded.app_version,
			description      = excluded.description,
			keywords_json    = excluded.keywords_json,
			maintainers_json = excluded.maintainers_json,
			size_bytes       = excluded.size_bytes,
			digest           = excluded.digest,
			filename         = excluded.filename,
			uploaded_at      = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, c.RepoID, c.Name, c.Version, c.AppVersion, c.Description,
		kw, mt, c.SizeBytes, c.Digest, c.Filename); err != nil {
		return 0, fmt.Errorf("helm_charts: upsert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM helm_charts WHERE repo_id=? AND name=? AND version=?`,
		c.RepoID, c.Name, c.Version,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("helm_charts: read-back: %w", err)
	}
	return id, nil
}

// Delete removes the row by id.
func (r *HelmChartsRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM helm_charts WHERE id=?`, id,
	); err != nil {
		return fmt.Errorf("helm_charts: delete %d: %w", id, err)
	}
	return nil
}

// FindByNameVersion returns the row matching (name, version) inside repoID.
func (r *HelmChartsRepo) FindByNameVersion(ctx context.Context, repoID int64, name, version string) (*HelmChart, error) {
	return r.scanOne(ctx, `repo_id=? AND name=? AND version=?`, repoID, name, version)
}

// FindByDigest returns the row matching digest inside repoID.
func (r *HelmChartsRepo) FindByDigest(ctx context.Context, repoID int64, digest string) (*HelmChart, error) {
	return r.scanOne(ctx, `repo_id=? AND digest=?`, repoID, digest)
}

// ListByRepo returns every chart row for repoID, ordered by name then
// semver descending (approximated via string DESC; index.yaml regen is
// the canonical ordering — this is only used for stable listings).
func (r *HelmChartsRepo) ListByRepo(ctx context.Context, repoID int64) ([]HelmChart, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, name, version, app_version, description,
		       keywords_json, maintainers_json, size_bytes, digest, filename, uploaded_at
		FROM helm_charts WHERE repo_id=?
		ORDER BY name, version DESC
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("helm_charts: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HelmChart
	for rows.Next() {
		c, err := scanHelmChart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *HelmChartsRepo) scanOne(ctx context.Context, where string, args ...any) (*HelmChart, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, name, version, app_version, description,
		       keywords_json, maintainers_json, size_bytes, digest, filename, uploaded_at
		FROM helm_charts WHERE `+where, args...)
	c, err := scanHelmChart(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("helm_charts: scan: %w", err)
	}
	return c, nil
}

func scanHelmChart(rs scanner) (*HelmChart, error) {
	var c HelmChart
	var uploaded string
	if err := rs.Scan(
		&c.ID, &c.RepoID, &c.Name, &c.Version, &c.AppVersion, &c.Description,
		&c.KeywordsJSON, &c.MaintainersJSON, &c.SizeBytes, &c.Digest, &c.Filename, &uploaded,
	); err != nil {
		return nil, err
	}
	c.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
	return &c, nil
}
