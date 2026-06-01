package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// AptSuite is one row of apt_suites — a (suite, component, architecture)
// triple scoped to one repo. deb_packages link here by suite_id so
// Packages/Release regeneration can partition cleanly.
type AptSuite struct {
	ID           int64
	RepoID       int64
	Suite        string
	Component    string
	Architecture string
}

// AptSuitesRepo owns CRUD on apt_suites. All writers take a *sql.Tx from
// DB.WriteTx so they can ride alongside the deb_packages upsert in the
// APT handler's single writer transaction.
type AptSuitesRepo struct{ db *DB }

// NewAptSuitesRepo constructs the repo bound to db.
func NewAptSuitesRepo(db *DB) *AptSuitesRepo { return &AptSuitesRepo{db: db} }

// Insert inserts (or fetches) the suite row. If the (repo_id, suite,
// component, arch) tuple already exists, the existing id is returned so
// callers never depend on DB error inspection for the idempotent case.
func (r *AptSuitesRepo) Insert(ctx context.Context, tx *sql.Tx, repoID int64, suite, component, arch string) (int64, error) {
	if suite == "" || component == "" || arch == "" {
		return 0, errors.New("apt_suites: suite/component/architecture required")
	}
	// Upsert via INSERT OR IGNORE then SELECT; preserves the UNIQUE constraint.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO apt_suites(repo_id, suite, component, architecture)
		VALUES (?, ?, ?, ?)
	`, repoID, suite, component, arch); err != nil {
		return 0, fmt.Errorf("apt_suites: insert: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM apt_suites
		WHERE repo_id=? AND suite=? AND component=? AND architecture=?
	`, repoID, suite, component, arch).Scan(&id); err != nil {
		return 0, fmt.Errorf("apt_suites: read-back: %w", err)
	}
	return id, nil
}

// InsertBatch inserts every row in one go. Rows are assumed to share the
// supplied repoID (the per-row RepoID is not consulted so callers can
// pass partially-populated structs).
func (r *AptSuitesRepo) InsertBatch(ctx context.Context, tx *sql.Tx, repoID int64, rows []AptSuite) error {
	for _, row := range rows {
		if _, err := r.Insert(ctx, tx, repoID, row.Suite, row.Component, row.Architecture); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes a suite row by id.
func (r *AptSuitesRepo) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM apt_suites WHERE id=?`, id,
	); err != nil {
		return fmt.Errorf("apt_suites: delete %d: %w", id, err)
	}
	return nil
}

// ListByRepo returns every suite row for repoID, ordered deterministically.
func (r *AptSuitesRepo) ListByRepo(ctx context.Context, repoID int64) ([]AptSuite, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, suite, component, architecture
		FROM apt_suites WHERE repo_id=?
		ORDER BY suite, component, architecture
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("apt_suites: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AptSuite
	for rows.Next() {
		var s AptSuite
		if err := rows.Scan(&s.ID, &s.RepoID, &s.Suite, &s.Component, &s.Architecture); err != nil {
			return nil, fmt.Errorf("apt_suites: scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// FindByTuple returns the suite with the matching (suite, component,
// arch) tuple scoped to repoID. Returns ErrNotFound when no row matches.
func (r *AptSuitesRepo) FindByTuple(ctx context.Context, repoID int64, suite, component, arch string) (*AptSuite, error) {
	var s AptSuite
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, suite, component, architecture
		FROM apt_suites WHERE repo_id=? AND suite=? AND component=? AND architecture=?
	`, repoID, suite, component, arch).Scan(&s.ID, &s.RepoID, &s.Suite, &s.Component, &s.Architecture)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("apt_suites: find: %w", err)
	}
	return &s, nil
}
