// Package api — admin maintenance mode endpoint (Phase 05-03, OPS-05).
//
// GET  /api/v1/admin/maintenance — returns current maintenance status.
// POST /api/v1/admin/maintenance — toggles maintenance mode on/off.
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// mountAdminMaintenance installs GET/POST /admin/maintenance on r.
func (d Deps) mountAdminMaintenance(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/maintenance", d.handleGetMaintenance)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/maintenance", d.handleToggleMaintenance)

	// ME-06: public read of maintenance status (enabled bool only) so the
	// banner is visible to non-admin users too. Toggled_by/toggled_at stay
	// admin-gated above.
	r.Get("/maintenance/status", d.handleMaintenanceStatus)
}

// handleMaintenanceStatus returns only the enabled flag — safe to expose to
// any authenticated user (wider: the router mounts this under the auth'd
// subtree, so no additional gate is needed).
func (d Deps) handleMaintenanceStatus(w http.ResponseWriter, r *http.Request) {
	enabled := false
	if v, err := d.Settings.Get(r.Context(), "maintenance_mode"); err == nil && v == "true" {
		enabled = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
}

func (d Deps) handleGetMaintenance(w http.ResponseWriter, r *http.Request) {
	enabled := false
	if v, err := d.Settings.Get(r.Context(), "maintenance_mode"); err == nil && v == "true" {
		enabled = true
	}
	var toggledBy, toggledAt string
	if v, err := d.Settings.Get(r.Context(), "maintenance_toggled_by"); err == nil {
		toggledBy = v
	}
	if v, err := d.Settings.Get(r.Context(), "maintenance_toggled_at"); err == nil {
		toggledAt = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    enabled,
		"toggled_by": toggledBy,
		"toggled_at": toggledAt,
	})
}

func (d Deps) handleToggleMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}

	val := "false"
	if req.Enabled {
		val = "true"
	}

	if err := d.Settings.Set(r.Context(), "maintenance_mode", val); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Record who toggled and when.
	actor, _ := auth.ActorFromContext(r.Context())
	login := ""
	if actor.ID != 0 {
		if u, err := d.Users.FindByID(r.Context(), actor.ID); err == nil {
			login = u.Login
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = d.Settings.Set(r.Context(), "maintenance_toggled_by", login)
	_ = d.Settings.Set(r.Context(), "maintenance_toggled_at", now)

	// Audit event.
	uid := actor.ID
	d.recordAudit(r, audit.Event{
		Kind:        audit.EvtMaintenanceToggled,
		ActorUserID: &uid,
		TargetKind:  "maintenance",
		Outcome:     val,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    req.Enabled,
		"toggled_by": login,
		"toggled_at": now,
	})
}
