// Package api — admin background-jobs summary endpoint (Phase 07 / D-06).
//
// GET /api/v1/admin/jobs/summary — super-admin only. Returns an at-a-glance
// snapshot of the sync_jobs work queue for the Phase 7 Dashboard C-4
// Background Jobs card. Shape is LOCKED at D-06 (CONTEXT.md) — do not add
// or remove keys without a CONTEXT change.
//
//	{
//	  "running":          int,
//	  "queued":            int,         // maps to sync_jobs.status='pending'
//	  "failed_last_24h":   int,
//	  "last_completed_at": "RFC3339" | null,
//	  "last_failed_at":    "RFC3339" | null
//	}
//
// Reuses the existing ActionTriggerGC super-admin gate (CONTEXT §D-06 — we
// do not introduce a new policy action for a read-only summary).
//
// Scope note: the endpoint surfaces only `sync_jobs` — the scan-pool has
// its own admin surface (admin_scans) and is not mixed in here. If Phase 7
// decides the C-4 card should aggregate across both pools, that's a
// CONTEXT-level decision, not a handler-layer change.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// jobsSummaryResponse is the LOCKED D-06 shape. Field names are snake_case
// (JSON tags) per the OmniRepo API convention; pointer timestamps marshal
// as `null` when the corresponding sync_jobs row set is empty.
type jobsSummaryResponse struct {
	Running         int        `json:"running"`
	Queued          int        `json:"queued"`
	FailedLast24h   int        `json:"failed_last_24h"`
	LastCompletedAt *time.Time `json:"last_completed_at"`
	LastFailedAt    *time.Time `json:"last_failed_at"`
}

// mountAdminJobs installs GET /admin/jobs/summary on r. Mirrors mountAdminGC
// shape so registration in admin_phase1.go is one-line consistent. Called
// from inside the phase-1 super-admin group (session/api-key middleware +
// membership resolver already applied by the parent chain).
//
// No-op when the deps this handler reads are not wired — the handler only
// queries d.DB.Reader which is always set, so the mount is unconditional.
func (d Deps) mountAdminJobs(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/jobs/summary", d.handleJobsSummary)
}

// handleJobsSummary returns the D-06 counts + two timestamps.
//
// Aggregate SQL uses three separate COUNT queries (one per bucket) rather
// than a single FILTER-clause aggregate. Rationale: modernc/sqlite v1.48
// supports `FILTER (WHERE ...)` (SQLite 3.51.x), but the per-bucket form
// is simpler to review, easier to explain in comments, and identical in
// performance on sync_jobs (small table, indexed on (status, updated_at)).
// It also side-steps the Assumption-A2 fallback the plan carried forward
// from RESEARCH — if FILTER syntax ever regressed we'd need to swap it in
// three places anyway.
func (d Deps) handleJobsSummary(w http.ResponseWriter, r *http.Request) {
	var resp jobsSummaryResponse

	// Count running jobs.
	if err := d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM sync_jobs WHERE status='running'`,
	).Scan(&resp.Running); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Count queued (pending) jobs. sync_jobs distinguishes pending (waiting
	// for lease) from running (currently leased); D-06 names the bucket
	// "queued" on the wire because that's what operators intuitively call
	// "jobs waiting to run".
	if err := d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM sync_jobs WHERE status='pending'`,
	).Scan(&resp.Queued); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Count failures in the last 24 hours. `updated_at` is flipped by
	// MarkPermanentlyFailed and MarkFailed paths in metadata/sync_jobs.go
	// so it is the canonical "when did this row last change state" column.
	if err := d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM sync_jobs
		  WHERE status='failed' AND updated_at > datetime('now','-1 day')`,
	).Scan(&resp.FailedLast24h); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// last_completed_at: most-recent updated_at among status='done' rows.
	var lastCompleted sql.NullTime
	if err := d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT updated_at FROM sync_jobs WHERE status='done' ORDER BY updated_at DESC LIMIT 1`,
	).Scan(&lastCompleted); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if lastCompleted.Valid {
		t := lastCompleted.Time
		resp.LastCompletedAt = &t
	}

	// last_failed_at: most-recent updated_at among status='failed' rows.
	var lastFailed sql.NullTime
	if err := d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT updated_at FROM sync_jobs WHERE status='failed' ORDER BY updated_at DESC LIMIT 1`,
	).Scan(&lastFailed); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if lastFailed.Valid {
		t := lastFailed.Time
		resp.LastFailedAt = &t
	}

	writeJSON(w, http.StatusOK, resp)
}
