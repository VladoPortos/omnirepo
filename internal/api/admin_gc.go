// Package api — admin GC endpoint (Phase 02-12, D-37, OPS-06).
//
// POST /api/v1/admin/gc — super-admin only. Validates that no GC job is
// currently pending or running (returns 409 already_running if so),
// otherwise enqueues a sync_jobs row with kind="gc" and returns 202 with
// the job id. The actual mark-and-sweep runs asynchronously on the sync
// pool (see internal/jobs/gc.go).
package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// GCDeps bundles the dependencies the admin GC endpoint needs beyond
// api.Deps. Wired by app.Run via Deps.GCDeps.
type GCDeps struct {
	SyncJobs *metadata.SyncJobsRepo
	SyncKick func() // sync pool kick; nil-safe
}

// mountAdminGC installs POST /admin/gc on r. No-op when GCDeps is nil.
func (d Deps) mountAdminGC(r chi.Router) {
	if d.GCDeps == nil || d.GCDeps.SyncJobs == nil {
		return
	}
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/gc", d.handleTriggerGC)
}

// gcAlreadyRunningCount returns the number of pending/running GC rows.
// Used to enforce the "one GC at a time" invariant (D-37).
func gcAlreadyRunningCount(ctx context.Context, db *metadata.DB) (int, error) {
	var n int
	err := db.Reader.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_jobs
		WHERE kind=? AND status IN ('pending','running')
	`, jobs.GCJobKind).Scan(&n)
	return n, err
}

// handleTriggerGC enqueues a fresh GC job after rejecting concurrent runs.
func (d Deps) handleTriggerGC(w http.ResponseWriter, r *http.Request) {
	// Concurrent-run guard. The check + insert race is bounded: even if
	// two callers slip through, the dispatcher serializes leases on the
	// writer pool. The 409 path saves a wasted job row in the common
	// single-admin case.
	n, err := gcAlreadyRunningCount(r.Context(), d.DB)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if n > 0 {
		writeJSONError(w, http.StatusConflict, ErrConflict, "gc already running")
		return
	}

	// Enqueue via SyncJobsRepo.Enqueue inside a writer tx.
	var jobID int64
	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		id, eerr := d.GCDeps.SyncJobs.Enqueue(r.Context(), tx, jobs.GCJobKind, 0, 0, "{}")
		if eerr != nil {
			return eerr
		}
		jobID = id
		return nil
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Best-effort kick so the dispatcher picks up immediately rather than
	// waiting for the next 2s poll.
	if d.GCDeps.SyncKick != nil {
		d.GCDeps.SyncKick()
	}

	// Audit gc.triggered (super-admin actor on ctx).
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtGCTriggered,
			ActorUserID: &uid,
			TargetKind:  "gc",
			TargetID:    strconv.FormatInt(jobID, 10),
			Outcome:     "ok",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"job_id":` + strconv.FormatInt(jobID, 10) + `}`))
}
