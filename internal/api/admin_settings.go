// Package api — admin settings CRUD endpoint (Phase 05-03).
//
// GET   /api/v1/admin/settings — returns all settings as key-value map.
// PATCH /api/v1/admin/settings — updates settings from a key-value map body.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// mountAdminSettings installs GET/PATCH /admin/settings on r.
func (d Deps) mountAdminSettings(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/settings", d.handleGetSettings)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Patch("/admin/settings", d.handlePatchSettings)
}

func (d Deps) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	all, err := d.Settings.GetAll(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, all)
}

func (d Deps) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var patch map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&patch); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if len(patch) == 0 {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "empty patch")
		return
	}

	// Protected settings cannot be modified through the REST API.
	var protectedSettings = map[string]bool{
		"upstream_creds_aead_key":  true,
		"docker_token_hmac_secret": true,
	}

	for k, v := range patch {
		if protectedSettings[k] {
			writeJSONError(w, http.StatusForbidden, ErrValidationFailed, k+" is a protected setting")
			return
		}
		if err := d.Settings.Set(r.Context(), k, v); err != nil {
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to set "+k)
			return
		}
	}

	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtProjectUpdated, // reuse generic update event
			ActorUserID: &uid,
			TargetKind:  "settings",
			Outcome:     "updated",
		})
	}

	// Return updated settings.
	all, err := d.Settings.GetAll(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, all)
}
