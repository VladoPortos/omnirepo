package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// JobView is the uniform in-memory projection a Pool worker sees.
// Both sync_jobs rows and scans rows are flattened into this shape so
// the Pool struct is agnostic to which queue backs it.
//
// For sync_jobs: Kind = SyncJob.Kind, Payload = SyncJob.PayloadJSON,
// RepoID + ProjectID from the row.
// For scans: Kind = Scan.ArtifactKind, Payload = Scan.ArtifactID,
// RepoID from the row, ProjectID always 0 (scans don't carry it).
type JobView struct {
	ID        int64
	Kind      string
	ProjectID int64
	RepoID    int64
	Payload   string
	Attempts  int64
	LeaseID   string
}

// LeaseRepo is the narrow Pool interface over an underlying sync_jobs /
// scans repository. Two adapters below (syncJobsAdapter, scansAdapter)
// implement it against the real repos.
type LeaseRepo interface {
	// LeaseOne atomically leases the next pending row. Must run against
	// the writer pool (single-statement UPDATE...RETURNING).
	LeaseOne(ctx context.Context, leaseID string) (*JobView, bool, error)

	// MarkDone flips status='done' for id. Runs inside tx.
	MarkDone(ctx context.Context, tx *sql.Tx, id int64) error

	// MarkFailed sets status='pending', increments attempts, records
	// errStr, and schedules next_run_at. Runs inside tx.
	MarkFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string, nextRunAt time.Time) error

	// MarkPermanentlyFailed sets status='failed' (terminal, no more
	// retries). Runs inside tx.
	MarkPermanentlyFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string) error

	// MarkPermanentlyFailedWithLog sets status='failed' AND populates
	// the log column atomically in ONE UPDATE. Used by the helm
	// partial-sync live path — see Pool.markFailed helm branch. For
	// adapters whose backing table has no log column (e.g. scans), the
	// implementation falls back to MarkPermanentlyFailed (the helm branch
	// never routes scan rows so logJSON is unreachable through this path
	// in practice).
	MarkPermanentlyFailedWithLog(ctx context.Context, tx *sql.Tx, id int64, errStr, logJSON string) error
}

// syncJobsAdapter wraps *metadata.SyncJobsRepo to satisfy LeaseRepo.
type syncJobsAdapter struct{ r *metadata.SyncJobsRepo }

// SyncJobsAdapter wraps a sync_jobs repo for the Pool's LeaseRepo interface.
func SyncJobsAdapter(r *metadata.SyncJobsRepo) LeaseRepo { return &syncJobsAdapter{r: r} }

func (a *syncJobsAdapter) LeaseOne(ctx context.Context, leaseID string) (*JobView, bool, error) {
	j, ok, err := a.r.LeaseOne(ctx, leaseID)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &JobView{
		ID:        j.ID,
		Kind:      j.Kind,
		ProjectID: j.ProjectID,
		RepoID:    j.RepoID,
		Payload:   j.PayloadJSON,
		Attempts:  j.Attempts,
		LeaseID:   j.LeaseID,
	}, true, nil
}

func (a *syncJobsAdapter) MarkDone(ctx context.Context, tx *sql.Tx, id int64) error {
	return a.r.MarkDone(ctx, tx, id)
}

func (a *syncJobsAdapter) MarkFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string, nextRunAt time.Time) error {
	return a.r.MarkFailed(ctx, tx, id, errStr, nextRunAt)
}

func (a *syncJobsAdapter) MarkPermanentlyFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string) error {
	return a.r.MarkPermanentlyFailed(ctx, tx, id, errStr)
}

// MarkPermanentlyFailedWithLog routes to the atomic terminal writer that
// sets status='failed' + log in ONE UPDATE. Used by the helm partial-sync
// live path.
func (a *syncJobsAdapter) MarkPermanentlyFailedWithLog(ctx context.Context, tx *sql.Tx, id int64, errStr, logJSON string) error {
	return a.r.MarkPermanentlyFailedWithLog(ctx, tx, id, errStr, logJSON)
}

// scansAdapter wraps *metadata.ScansRepo to satisfy LeaseRepo. Scan rows'
// ArtifactKind / ArtifactID become the Pool's Kind / Payload.
type scansAdapter struct{ r *metadata.ScansRepo }

// ScansAdapter wraps a scans repo for the Pool's LeaseRepo interface.
func ScansAdapter(r *metadata.ScansRepo) LeaseRepo { return &scansAdapter{r: r} }

func (a *scansAdapter) LeaseOne(ctx context.Context, leaseID string) (*JobView, bool, error) {
	s, ok, err := a.r.LeaseOne(ctx, leaseID)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &JobView{
		ID:       s.ID,
		Kind:     s.ArtifactKind,
		RepoID:   s.RepoID,
		Payload:  s.ArtifactID,
		Attempts: s.Attempts,
		LeaseID:  s.LeaseID,
	}, true, nil
}

func (a *scansAdapter) MarkDone(ctx context.Context, tx *sql.Tx, id int64) error {
	// Scans.MarkDone requires summary/sbom/dbversion; the scan handler
	// ALWAYS calls ScansRepo.MarkDone directly inside its own writer tx,
	// populating severity_summary_json + sbom_path + trivy_db_version.
	// The pool's generic success path then calls here as a terminal
	// fallback — but writing "{}" unconditionally would clobber the
	// handler's populated row.
	//
	// Check status='running' before overwriting: if the row has
	// already flipped to 'done' by the handler, this is a no-op.
	res, err := tx.ExecContext(ctx, `
		UPDATE scans
		SET status='done', updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='running'
	`, id)
	if err != nil {
		return fmt.Errorf("scans: generic mark_done %d: %w", id, err)
	}
	// RowsAffected==0 means the handler already marked it done; that's
	// the happy path. Either way, success.
	_ = res
	return nil
}

func (a *scansAdapter) MarkFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string, nextRunAt time.Time) error {
	return a.r.MarkFailed(ctx, tx, id, errStr, nextRunAt)
}

func (a *scansAdapter) MarkPermanentlyFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string) error {
	return a.r.MarkPermanentlyFailed(ctx, tx, id, errStr)
}

// MarkPermanentlyFailedWithLog — scans has no log column, so logJSON is
// ignored and we fall back to MarkPermanentlyFailed. The helm-sync
// branch in Pool.markFailed only fires for kind='helm_sync', which
// never lands on the scan pool; this method exists solely so
// scansAdapter continues to satisfy the extended LeaseRepo interface.
func (a *scansAdapter) MarkPermanentlyFailedWithLog(ctx context.Context, tx *sql.Tx, id int64, errStr, logJSON string) error {
	_ = logJSON
	return a.r.MarkPermanentlyFailed(ctx, tx, id, errStr)
}
