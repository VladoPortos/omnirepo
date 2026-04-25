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

	// Phase 8 Plan 01 / 02 (MIRROR-08..12) — byte-level progress tracking.
	// Workers call SetProgress to advance; the REST /jobs/{id} endpoint
	// reads the triple and renders it for the UI polling path. total_bytes
	// = 0 is legal (Helm sync is step-based per D-11).
	ProgressBytes int64
	TotalBytes    int64
	CurrentStep   string
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

// MarkPermanentlyFailedWithLog flips status='failed' AND populates the
// log column atomically in a single UPDATE statement. Used by the
// Phase 5 helm partial-sync live-path (HELMRETRY-03, D-04): the pool's
// markFailed helm branch calls this after recognising a PartialSyncErr
// so the sync_jobs row carries the partial-progress JSON
// ({"partial":true,"files_persisted":N,"files_expected":M}) in the
// same write that flips the terminal status.
//
// Atomicity invariant (D-04): any reader observing status='failed' on
// a row written by this method ALWAYS observes the populated log
// column too — they are set in ONE tx.ExecContext call, so no
// intermediate two-UPDATE window can expose status-without-log.
// Enforced by TestSyncJobsRepo_MarkPermanentlyFailedWithLog via a
// single SELECT status, last_error, log ... Scan(...) call.
//
// leased_by / leased_at are intentionally NOT cleared (unlike
// RecoverStale): this is a terminal write, so stale lease values
// become informative residue for post-mortem debugging (which worker
// was running, when it started).
func (r *SyncJobsRepo) MarkPermanentlyFailedWithLog(
	ctx context.Context, tx *sql.Tx,
	id int64, errMsg, logJSON string,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE sync_jobs
		SET status='failed',
		    last_error = ?,
		    log        = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, errMsg, logJSON, id)
	if err != nil {
		return fmt.Errorf("sync_jobs: mark_perm_failed_with_log %d: %w", id, err)
	}
	return nil
}

// SetProgress persists the (step, done, total) triple for a running job.
// Runs against the writer pool without a caller-supplied tx — progress
// writes are fire-and-forget from the protocol handlers and must not
// block or participate in the outer sync tx. Throttling (one write per
// 200 ms with change detection) is enforced by internal/jobs/progress
// (plan 08-02); this helper is the single SQL mutator.
//
// total == 0 is legal (Helm step-based progress per D-11). step may be
// empty (initial "starting" transition). progress_bytes is capped at
// total_bytes at the call site when total > 0 — this helper writes the
// triple as given.
func (r *SyncJobsRepo) SetProgress(ctx context.Context, jobID int64, step string, done, total int64) error {
	_, err := r.db.Writer.ExecContext(ctx, `
		UPDATE sync_jobs
		SET progress_bytes = ?,
		    total_bytes    = ?,
		    current_step   = ?,
		    updated_at     = CURRENT_TIMESTAMP
		WHERE id = ?
	`, done, total, step, jobID)
	if err != nil {
		return fmt.Errorf("sync_jobs: set progress %d: %w", jobID, err)
	}
	return nil
}

// SetFilesSynced persists files_synced for a job. Called once at the end
// of a successful sync by each protocol handler (pypi, helm, rpm, deb)
// right after the terminal progress.Set(...,"done",...) — one write per
// job, no throttling needed. Kept out of SyncProgressRepo/ProgressWriter
// (which handle the hot-loop byte-progress path) so adding this field
// doesn't force every SyncProgressRepo test fake to widen.
//
// Semantics: files is the count of newly-stored files for this sync
// (matches each handler's existing filesAdded counter — cached/skipped
// files are NOT counted). This is what the UI's success pill renders
// as "Sync complete · N files · X MB" (D-03 closure).
func (r *SyncJobsRepo) SetFilesSynced(ctx context.Context, jobID, files int64) error {
	_, err := r.db.Writer.ExecContext(ctx, `
		UPDATE sync_jobs
		SET files_synced = ?,
		    updated_at   = CURRENT_TIMESTAMP
		WHERE id = ?
	`, files, jobID)
	if err != nil {
		return fmt.Errorf("sync_jobs: set files_synced %d: %w", jobID, err)
	}
	return nil
}

// SetSummaryDriftPurged merges a `drift_purged` integer key into the
// sync_jobs.summary JSON blob (v1.5 Phase 6 / DRIFTPURGE-03, D-21).
// Emitted unconditionally by each protocol sync_handler after a
// successful drift run (including count=0 — run evidence per D-10).
// Absent when drift didn't run (guard tripped, drift_purge=false, or
// non-mirror repo).
//
// Uses SQLite's json_set() so repeat calls overwrite the key in place
// and sibling keys from other writers (future summary additions) are
// preserved. json1 is compiled into modernc.org/sqlite by default.
func (r *SyncJobsRepo) SetSummaryDriftPurged(ctx context.Context, jobID, count int64) error {
	_, err := r.db.Writer.ExecContext(ctx, `
		UPDATE sync_jobs
		SET summary    = json_set(summary, '$.drift_purged', ?),
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, count, jobID)
	if err != nil {
		return fmt.Errorf("sync_jobs: set summary.drift_purged %d: %w", jobID, err)
	}
	return nil
}

// CountRepoInflight reports how many sync_jobs rows for repoID are
// currently pending or running. Used by the mirror-aware /sync endpoint
// (Phase 8 Plan 01, D-07) to enforce one-in-flight-sync-per-repo with
// a 409 envelope (plan 11-05 D-11 generalized this to
// `mirror.sync.in_flight`).
//
// Reader-pool variant (fast-path pre-check). Callers that MUST enforce
// "exactly one in-flight at a time" should use CountRepoInflightTx within
// the same writer tx as Enqueue — modernc SQLite serialises writer-pool
// statements so the check+insert pair becomes atomic. See plan 08-06
// Codex rescue Q7 for history.
func (r *SyncJobsRepo) CountRepoInflight(ctx context.Context, repoID int64) (int, error) {
	var n int
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_jobs
		WHERE repo_id = ? AND status IN ('pending','running')
	`, repoID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sync_jobs: count inflight %d: %w", repoID, err)
	}
	return n, nil
}

// InflightJob summarizes the currently-running (or pending) sync for a
// repo. Used by internal/httpx/sync_rest.go to populate the D-11
// generalized 409 envelope `mirror.sync.in_flight` with details
// `{kind, job_id, started_at}`.
//
// StartedAt is sourced from `COALESCE(leased_at, created_at)`. The
// sync_jobs schema (migration 002) does not carry a dedicated
// `started_at` column — leased_at is populated at LeaseOne time, while
// created_at defaults to CURRENT_TIMESTAMP at Enqueue. The coalesce
// gives the UI a stable RFC3339 timestamp for both pending (not yet
// leased) and running (leased) rows.
type InflightJob struct {
	ID        int64
	Kind      string
	StartedAt time.Time
}

// GetInflightTx returns the newest pending/running sync job for repoID,
// or (InflightJob{}, false, nil) when none. Intended to be called inside
// the same DB.WriteTx as the subsequent Enqueue so a concurrent /sync
// POST either (a) observes the first tx's pending row and short-circuits
// with a 409, or (b) commits first and makes the second caller observe
// an in-flight row.
//
// Sort is `ORDER BY id DESC` so the caller sees the most recently
// enqueued row — this matches the pooled "one writer serializes"
// semantics of modernc SQLite and keeps the 409 envelope deterministic
// when (pathologically) multiple pending rows exist for the same repo.
//
// Kind-agnostic: no `kind = ?` clause in the WHERE, identical filter
// shape to CountRepoInflightTx. INV-11-05-03: plans 11-06 + future
// kind additions must not tighten this filter.
func (r *SyncJobsRepo) GetInflightTx(ctx context.Context, tx *sql.Tx, repoID int64) (InflightJob, bool, error) {
	const q = `
		SELECT id, kind, COALESCE(leased_at, created_at) AS started_at
		FROM sync_jobs
		WHERE repo_id = ? AND status IN ('pending','running')
		ORDER BY id DESC
		LIMIT 1
	`
	var job InflightJob
	// modernc.org/sqlite stores TIMESTAMP columns as TEXT in SQLite's
	// native format "YYYY-MM-DD HH:MM:SS" (from CURRENT_TIMESTAMP) or, if
	// a Go time.Time was passed directly, as the driver-stringified form
	// "YYYY-MM-DD HH:MM:SS.nnnnnnnnn +0000 UTC" (see scans.go:95-104 for
	// the F-T6 wedge we hit last time this bit us). Scanning into
	// sql.NullTime fails on both formats, so scan as string and parse
	// by trying the native shape first, then the driver-stringified
	// shape as a fallback.
	var startedAt sql.NullString
	err := tx.QueryRowContext(ctx, q, repoID).Scan(&job.ID, &job.Kind, &startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InflightJob{}, false, nil
	}
	if err != nil {
		return InflightJob{}, false, fmt.Errorf("sync_jobs: get_inflight %d: %w", repoID, err)
	}
	if startedAt.Valid && startedAt.String != "" {
		job.StartedAt = parseSyncJobTimestamp(startedAt.String)
	}
	return job, true, nil
}

// parseSyncJobTimestamp parses the TEXT-stored sync_jobs timestamp back
// into a time.Time. Falls through the two shapes modernc.org/sqlite
// actually emits for this column; any unparseable value produces the
// zero time (the 409 envelope still renders — just without a valid
// started_at — rather than failing the whole request).
func parseSyncJobTimestamp(raw string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",                    // SQLite CURRENT_TIMESTAMP
		"2006-01-02 15:04:05.999999999 -0700 MST", // time.Time.String()
		"2006-01-02T15:04:05Z",                    // RFC3339 (future-proof)
		"2006-01-02T15:04:05.000Z",                // milli-UTC (future-proof)
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// CountRepoInflightTx is the tx-scoped variant of CountRepoInflight,
// intended to be called inside the same DB.WriteTx as the subsequent
// Enqueue so the check+insert pair is atomic against concurrent /sync
// POSTs (plan 08-06 Codex rescue Q7). SQLite serialises writer-pool
// statements; running the COUNT inside the tx guarantees a second caller
// sees the first caller's pending INSERT before it gets a chance to
// observe inflight=0.
func (r *SyncJobsRepo) CountRepoInflightTx(ctx context.Context, tx *sql.Tx, repoID int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_jobs
		WHERE repo_id = ? AND status IN ('pending','running')
	`, repoID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sync_jobs: count inflight tx %d: %w", repoID, err)
	}
	return n, nil
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

// RecoverStaleByKind is the kind-scoped counterpart to RecoverStale
// introduced for Phase 5 boot recovery (HELMRETRY-03, D-03b). Rather
// than re-pending stale 'running' rows, it terminates them at the
// caller-chosen terminalStatus ('failed' for helm_sync per D-01) and
// populates sync_jobs.log with the caller-supplied JSON in the SAME
// UPDATE statement (D-04 atomicity). The kind filter in the WHERE
// clause preserves the D-02 scope boundary: non-matching sync kinds
// retain the existing generic RecoverStale retry semantics.
//
// last_error is set to a stable sentinel ("stale running row
// terminated at boot") so Phase 7 drift-purge / Phase 6 scheduler
// consumers can distinguish crash-path terminations from the live-
// path PartialSyncErr writes made by MarkPermanentlyFailedWithLog.
//
// leased_by / leased_at are intentionally NOT cleared — this is a
// terminal write, so stale lease values remain as forensic residue.
//
// Returns the number of rows affected.
func (r *SyncJobsRepo) RecoverStaleByKind(
	ctx context.Context, tx *sql.Tx,
	olderThan time.Time, kind, terminalStatus, logJSON string,
) (int, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE sync_jobs
		SET status     = ?,
		    log        = ?,
		    last_error = 'stale running row terminated at boot',
		    updated_at = CURRENT_TIMESTAMP
		WHERE status='running' AND kind=? AND leased_at < ?
	`, terminalStatus, logJSON, kind, olderThan.UTC())
	if err != nil {
		return 0, fmt.Errorf("sync_jobs: recover_stale_by_kind %s: %w", kind, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
