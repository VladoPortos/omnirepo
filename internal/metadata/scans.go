package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ScansRepo owns scans rows (scan pool work queue + result envelope).
// Mirrors SyncJobsRepo's lease pattern (D-15) — single-statement
// UPDATE ... RETURNING, writer pool only.
type ScansRepo struct{ db *DB }

// Scan is the in-memory projection of a leased scan row.
type Scan struct {
	ID           int64
	RepoID       int64
	ArtifactKind string
	ArtifactID   string
	Attempts     int64
	LeaseID      string
}

// ScanResult is the aggregated outcome used by the block_on_severity
// gate and the REST API.
type ScanResult struct {
	ScanID              int64
	SeveritySummaryJSON string
	FinishedAt          time.Time
}

// NewScansRepo constructs a repo bound to db.
func NewScansRepo(db *DB) *ScansRepo { return &ScansRepo{db: db} }

// Enqueue inserts a pending scan row.
func (r *ScansRepo) Enqueue(ctx context.Context, tx *sql.Tx, repoID int64, artifactKind, artifactID string) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO scans(repo_id, artifact_kind, artifact_id, status)
		VALUES (?, ?, ?, 'pending')
	`, repoID, artifactKind, artifactID)
	if err != nil {
		return 0, fmt.Errorf("scans: enqueue: %w", err)
	}
	return res.LastInsertId()
}

// LeaseOne atomically leases the oldest due pending scan.
func (r *ScansRepo) LeaseOne(ctx context.Context, leaseID string) (*Scan, bool, error) {
	var s Scan
	err := r.db.Writer.QueryRowContext(ctx, `
		UPDATE scans
		SET status='running',
		    leased_by=?,
		    leased_at=CURRENT_TIMESTAMP,
		    started_at=CURRENT_TIMESTAMP,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id = (
		    SELECT id FROM scans
		    WHERE status='pending' AND next_run_at <= CURRENT_TIMESTAMP
		    ORDER BY next_run_at LIMIT 1
		)
		RETURNING id, repo_id, artifact_kind, artifact_id, attempts
	`, leaseID).Scan(&s.ID, &s.RepoID, &s.ArtifactKind, &s.ArtifactID, &s.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("scans: lease: %w", err)
	}
	s.LeaseID = leaseID
	return &s, true, nil
}

// MarkDone records the scan's final result and flips status='done'.
func (r *ScansRepo) MarkDone(ctx context.Context, tx *sql.Tx, id int64, severitySummaryJSON, sbomPath, trivyDBVersion string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE scans
		SET status='done',
		    finished_at=CURRENT_TIMESTAMP,
		    severity_summary_json=?,
		    sbom_path=?,
		    trivy_db_version=?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?
	`, severitySummaryJSON, sbomPath, trivyDBVersion, id)
	if err != nil {
		return fmt.Errorf("scans: mark_done %d: %w", id, err)
	}
	return nil
}

// MarkFailed records err and schedules a retry at nextRunAt (D-18).
func (r *ScansRepo) MarkFailed(ctx context.Context, tx *sql.Tx, id int64, errMsg string, nextRunAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE scans
		SET status='pending',
		    attempts=attempts+1,
		    last_error=?,
		    next_run_at=?,
		    leased_by='',
		    leased_at=NULL,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?
	`, errMsg, nextRunAt.UTC(), id)
	if err != nil {
		return fmt.Errorf("scans: mark_failed %d: %w", id, err)
	}
	return nil
}

// MarkPermanentlyFailed terminates the retry loop.
func (r *ScansRepo) MarkPermanentlyFailed(ctx context.Context, tx *sql.Tx, id int64, errMsg string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE scans
		SET status='failed', last_error=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE id=?
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("scans: mark_perm_failed %d: %w", id, err)
	}
	return nil
}

// RecoverStale mirrors SyncJobsRepo.RecoverStale.
func (r *ScansRepo) RecoverStale(ctx context.Context, tx *sql.Tx, olderThan time.Time) (int, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE scans
		SET status='pending', leased_by='', leased_at=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE status='running' AND leased_at < ?
	`, olderThan.UTC())
	if err != nil {
		return 0, fmt.Errorf("scans: recover_stale: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// LatestForArtifact returns the most recently finished scan for an
// artifact, or (nil, nil) if none. Feeds the block_on_severity gate
// (D-26).
func (r *ScansRepo) LatestForArtifact(ctx context.Context, repoID int64, artifactKind, artifactID string) (*ScanResult, error) {
	var out ScanResult
	var finished sql.NullTime
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, severity_summary_json, finished_at
		FROM scans
		WHERE repo_id=? AND artifact_kind=? AND artifact_id=? AND status='done'
		ORDER BY finished_at DESC
		LIMIT 1
	`, repoID, artifactKind, artifactID).Scan(&out.ScanID, &out.SeveritySummaryJSON, &finished)
	if finished.Valid {
		out.FinishedAt = finished.Time
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scans: latest: %w", err)
	}
	return &out, nil
}
