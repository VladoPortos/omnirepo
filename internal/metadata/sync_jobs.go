package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SyncJobsRepo owns sync_jobs rows (the sync pool's work queue).
//
// LeaseOne uses a single-statement UPDATE ... RETURNING so two dispatchers
// can never lease the same row (T-02-01-02). This method MUST be called
// against db.Writer (pool size 1) — calling it through the reader pool
// would deadlock against the writer.
type SyncJobsRepo struct{ db *DB }

// SyncJob is the in-memory projection of a leased sync_jobs row.
type SyncJob struct {
	ID         int64
	Kind       string
	ProjectID  int64 // 0 when NULL
	RepoID     int64 // 0 when NULL
	PayloadJSON string
	Attempts   int64
	LeaseID    string
}

// NewSyncJobsRepo constructs a repo bound to db.
func NewSyncJobsRepo(db *DB) *SyncJobsRepo { return &SyncJobsRepo{db: db} }

// Enqueue inserts a pending job and returns its id.
func (r *SyncJobsRepo) Enqueue(ctx context.Context, tx *sql.Tx, kind string, projectID, repoID int64, payloadJSON string) (int64, error) {
	var pid, rid any
	if projectID != 0 {
		pid = projectID
	}
	if repoID != 0 {
		rid = repoID
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, project_id, repo_id, payload_json, status)
		VALUES (?, ?, ?, ?, 'pending')
	`, kind, pid, rid, payloadJSON)
	if err != nil {
		return 0, fmt.Errorf("sync_jobs: enqueue: %w", err)
	}
	return res.LastInsertId()
}

// LeaseOne atomically leases the oldest due pending job to leaseID.
// Returns (nil, false, nil) when no row is available. Must run against
// the writer pool.
func (r *SyncJobsRepo) LeaseOne(ctx context.Context, leaseID string) (*SyncJob, bool, error) {
	var j SyncJob
	err := r.db.Writer.QueryRowContext(ctx, `
		UPDATE sync_jobs
		SET status='running',
		    leased_by=?,
		    leased_at=CURRENT_TIMESTAMP,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id = (
		    SELECT id FROM sync_jobs
		    WHERE status='pending' AND next_run_at <= CURRENT_TIMESTAMP
		    ORDER BY next_run_at LIMIT 1
		)
		RETURNING id, kind, COALESCE(project_id,0), COALESCE(repo_id,0), payload_json, attempts
	`, leaseID).Scan(&j.ID, &j.Kind, &j.ProjectID, &j.RepoID, &j.PayloadJSON, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sync_jobs: lease: %w", err)
	}
	j.LeaseID = leaseID
	return &j, true, nil
}

// MarkDone flips the job to status='done'.
func (r *SyncJobsRepo) MarkDone(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE sync_jobs SET status='done', updated_at=CURRENT_TIMESTAMP WHERE id=?
	`, id)
	if err != nil {
		return fmt.Errorf("sync_jobs: mark_done %d: %w", id, err)
	}
	return nil
}

// MarkFailed records err and schedules a retry at nextRunAt. The caller
// owns the backoff policy (D-18); this method does not compute it.
// Sets status back to 'pending' for future lease.
//
// Timestamp format: format explicitly as "YYYY-MM-DD HH:MM:SS" so the
// string comparison against CURRENT_TIMESTAMP in the lease poll works
// (see ScansRepo.MarkFailed for the full explanation — same class of
// bug, same fix; both paths were wedging retries forever).
func (r *SyncJobsRepo) MarkFailed(ctx context.Context, tx *sql.Tx, id int64, errMsg string, nextRunAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE sync_jobs
		SET status='pending',
		    attempts = attempts + 1,
		    last_error = ?,
		    next_run_at = ?,
		    leased_by='',
		    leased_at=NULL,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?
	`, errMsg, nextRunAt.UTC().Format("2006-01-02 15:04:05"), id)
	if err != nil {
		return fmt.Errorf("sync_jobs: mark_failed %d: %w", id, err)
	}
	return nil
}

// MarkPermanentlyFailed sets the job to status='failed' (no more
// retries). Used once attempts >= retry cap (D-18).
func (r *SyncJobsRepo) MarkPermanentlyFailed(ctx context.Context, tx *sql.Tx, id int64, errMsg string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE sync_jobs
		SET status='failed',
		    last_error = ?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("sync_jobs: mark_perm_failed %d: %w", id, err)
	}
	return nil
}

// RecoverStale sweeps running rows whose leased_at is older than the
// supplied threshold back to 'pending' (SYNC-03, D-19). Returns the
// number of rows affected.
func (r *SyncJobsRepo) RecoverStale(ctx context.Context, tx *sql.Tx, olderThan time.Time) (int, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE sync_jobs
		SET status='pending', leased_by='', leased_at=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE status='running' AND leased_at < ?
	`, olderThan.UTC())
	if err != nil {
		return 0, fmt.Errorf("sync_jobs: recover_stale: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
