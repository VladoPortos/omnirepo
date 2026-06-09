package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MavenArtifact mirrors one maven_artifacts row: a primary artifact file
// (.jar/.pom/...) deployed into a Maven repo. Path is the repo-relative
// storage path and the stable identity; GAV columns are parsed from it.
type MavenArtifact struct {
	ID         int64
	RepoID     int64
	GroupID    string
	ArtifactID string
	Version    string
	Classifier string
	Extension  string
	Filename   string
	Path       string
	SizeBytes  int64
	SHA256     string
	UploadedAt time.Time
}

// MavenArtifactsRepo owns CRUD on maven_artifacts.
type MavenArtifactsRepo struct{ db *DB }

// NewMavenArtifactsRepo constructs the repo bound to db.
func NewMavenArtifactsRepo(db *DB) *MavenArtifactsRepo { return &MavenArtifactsRepo{db: db} }

// Upsert inserts or replaces by (repo_id, path). Maven redeploys
// (SNAPSHOT and release re-deploys alike) overwrite in place, matching
// every standard Maven repository's behavior.
func (r *MavenArtifactsRepo) Upsert(ctx context.Context, tx *sql.Tx, a *MavenArtifact) (int64, error) {
	if a == nil {
		return 0, errors.New("maven_artifacts: nil artifact")
	}
	if a.Path == "" || a.GroupID == "" || a.ArtifactID == "" || a.Version == "" || a.Extension == "" {
		return 0, errors.New("maven_artifacts: path/group/artifact/version/extension required")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO maven_artifacts(
			repo_id, group_id, artifact_id, version, classifier,
			extension, filename, path, size_bytes, sha256
		) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, path) DO UPDATE SET
			group_id    = excluded.group_id,
			artifact_id = excluded.artifact_id,
			version     = excluded.version,
			classifier  = excluded.classifier,
			extension   = excluded.extension,
			filename    = excluded.filename,
			size_bytes  = excluded.size_bytes,
			sha256      = excluded.sha256,
			uploaded_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, a.RepoID, a.GroupID, a.ArtifactID, a.Version, a.Classifier,
		a.Extension, a.Filename, a.Path, a.SizeBytes, a.SHA256); err != nil {
		return 0, fmt.Errorf("maven_artifacts: upsert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM maven_artifacts WHERE repo_id=? AND path=?`,
		a.RepoID, a.Path,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("maven_artifacts: read-back: %w", err)
	}
	return id, nil
}

// Delete removes the row by id.
func (r *MavenArtifactsRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM maven_artifacts WHERE id=?`, id); err != nil {
		return fmt.Errorf("maven_artifacts: delete %d: %w", id, err)
	}
	return nil
}

// FindByPath returns the row matching path inside repoID, or ErrNotFound.
func (r *MavenArtifactsRepo) FindByPath(ctx context.Context, repoID int64, path string) (*MavenArtifact, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, group_id, artifact_id, version, classifier,
		       extension, filename, path, size_bytes, sha256, uploaded_at
		FROM maven_artifacts WHERE repo_id=? AND path=?
	`, repoID, path)
	a, err := scanMavenArtifact(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("maven_artifacts: scan: %w", err)
	}
	return a, nil
}

// ListByRepo returns every artifact row for repoID ordered by GAV.
func (r *MavenArtifactsRepo) ListByRepo(ctx context.Context, repoID int64) ([]MavenArtifact, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, group_id, artifact_id, version, classifier,
		       extension, filename, path, size_bytes, sha256, uploaded_at
		FROM maven_artifacts WHERE repo_id=?
		ORDER BY group_id, artifact_id, version, filename
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("maven_artifacts: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MavenArtifact
	for rows.Next() {
		a, err := scanMavenArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("maven_artifacts: scan: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func scanMavenArtifact(rs scanner) (*MavenArtifact, error) {
	var a MavenArtifact
	var uploaded string
	if err := rs.Scan(
		&a.ID, &a.RepoID, &a.GroupID, &a.ArtifactID, &a.Version, &a.Classifier,
		&a.Extension, &a.Filename, &a.Path, &a.SizeBytes, &a.SHA256, &uploaded,
	); err != nil {
		return nil, err
	}
	a.UploadedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", uploaded)
	return &a, nil
}
