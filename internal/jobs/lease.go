package jobs

import (
	"context"
	"database/sql"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
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
// implement it against the real Phase 02-01 repos.
type LeaseRepo interface {
	// LeaseOne atomically leases the next pending row. Must run against
	// the writer pool (single-statement UPDATE...RETURNING, D-15).
	LeaseOne(ctx context.Context, leaseID string) (*JobView, bool, error)

	// MarkDone flips status='done' for id. Runs inside tx.
	MarkDone(ctx context.Context, tx *sql.Tx, id int64) error

	// MarkFailed sets status='pending', increments attempts, records
	// errStr, and schedules next_run_at. Runs inside tx.
	MarkFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string, nextRunAt time.Time) error

	// MarkPermanentlyFailed sets status='failed' (terminal, no more
	// retries). Runs inside tx.
	MarkPermanentlyFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string) error
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
	// Scans.MarkDone requires summary/sbom/dbversion; the generic Pool
	// path only hits here when a handler returned nil and didn't populate
	// those fields itself. That's a misuse — scan handlers are
	// responsible for calling ScansRepo.MarkDone directly (inside their
	// own writer tx) with the summary. The pool's generic MarkDone path
	// is still safe: we record an empty summary + empty paths so the row
	// flips to 'done' without data loss beyond what the handler chose
	// not to supply. Phase 02-09 handler will bypass this generic path.
	return a.r.MarkDone(ctx, tx, id, "{}", "", "")
}

func (a *scansAdapter) MarkFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string, nextRunAt time.Time) error {
	return a.r.MarkFailed(ctx, tx, id, errStr, nextRunAt)
}

func (a *scansAdapter) MarkPermanentlyFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string) error {
	return a.r.MarkPermanentlyFailed(ctx, tx, id, errStr)
}
