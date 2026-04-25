// Package api — admin sweep endpoint for orphan S3 multipart uploads.
//
// POST /api/v1/admin/maintenance/sweep-multipart — super-admin gated via
// ActionTriggerGC. Honors the no-in-process-scheduler invariant by
// exposing the same SweepOrphanMultiparts function the boot goroutine
// uses to external schedulers (crontab / systemd timer / k8s CronJob).
// Plan 02-04 (S3HARD-08); CLAUDE.md §Constraints "no in-process
// schedulers".
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// defaultMultipartRetention matches both cfg.S3.MultipartRetention's
// default (config.Defaults) and the legacy hardcoded sweep cutoff so the
// endpoint never sweeps with a 0-second cutoff that would abort every
// in-flight upload.
const defaultMultipartRetention = 24 * time.Hour

// mountAdminSweepMultipart installs the on-demand orphan-multipart sweep
// route under /api/v1. Called from admin_phase1.go's Mount alongside the
// other super-admin maintenance routes.
func (d Deps) mountAdminSweepMultipart(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/maintenance/sweep-multipart", d.handleSweepMultipart)
}

// handleSweepMultipart drives backend.SweepOrphanMultiparts(ctx, cutoff)
// once and returns swept_uploads / cleaned_dirs / duration_ms as JSON.
//
// Cutoff: now - max(d.S3MultipartRetention, 24h). The clamp prevents an
// operator who blanks the config knob from accidentally aborting every
// in-flight upload. cfg.Validate() rejects negative values upstream so we
// only need to defend against zero here.
//
// nil-safe: when d.S3Backend is unset (test server that never wired it)
// the handler returns 500 InternalError so misconfiguration surfaces.
func (d Deps) handleSweepMultipart(w http.ResponseWriter, r *http.Request) {
	if d.S3Backend == nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	start := time.Now()
	retention := d.S3MultipartRetention
	if retention <= 0 {
		retention = defaultMultipartRetention
	}
	cutoff := time.Now().Add(-retention)

	swept, cleaned, err := d.S3Backend.SweepOrphanMultiparts(r.Context(), cutoff)
	if err != nil {
		slog.WarnContext(r.Context(), "s3.multipart.admin_sweep.error", "err", err)
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"swept_uploads": swept,
		"cleaned_dirs":  cleaned,
		"duration_ms":   time.Since(start).Milliseconds(),
	})
}
