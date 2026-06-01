// Package api — profile endpoints (Phase 05-04).
//
// PATCH /api/v1/me — update email, avatar_seed with diff-audit.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	authmw "github.com/vladoportos/omnirepo/internal/auth/middleware"
)

// mountProfile installs profile endpoints.
func (d Deps) mountProfile(r chi.Router) {
	r.With(authmw.RequireCanWith(auth.ActionEditOwnUser, func(r *http.Request) auth.Target {
		if a, ok := auth.ActorFromContext(r.Context()); ok {
			return auth.Target{Kind: "user", UserID: a.ID}
		}
		return auth.Target{}
	})).Patch("/me", d.handlePatchMe)
}

// patchMeRequest is the body shape for PATCH /me.
type patchMeRequest struct {
	Email      *string `json:"email,omitempty"`
	AvatarSeed *string `json:"avatar_seed,omitempty"`
}

func (d Deps) handlePatchMe(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	var req patchMeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}

	u, err := d.Users.FindByID(r.Context(), actor.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	// LO-01: collect diffs and apply in a single tx so a partial failure
	// never leaves the user half-updated (and the audit entry always
	// matches what actually committed).
	diff := map[string]any{}
	var emailPtr, avatarPtr *string

	if req.Email != nil && *req.Email != u.Email {
		if *req.Email == "" {
			writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "email empty")
			return
		}
		diff["email"] = map[string]any{"from": u.Email, "to": *req.Email}
		emailPtr = req.Email
	}

	if req.AvatarSeed != nil && *req.AvatarSeed != u.AvatarSeed {
		diff["avatar_seed"] = map[string]any{"from": u.AvatarSeed, "to": *req.AvatarSeed}
		avatarPtr = req.AvatarSeed
	}

	if emailPtr != nil || avatarPtr != nil {
		if err := d.Users.UpdateProfile(r.Context(), actor.ID, emailPtr, avatarPtr); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
	}

	if len(diff) > 0 {
		uid := actor.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtUserUpdated,
			ActorUserID: &uid,
			TargetKind:  "user",
			TargetID:    u.Login,
			Details:     map[string]any{"diff": diff},
		})
	}

	// Reload user for response.
	updated, err := d.Users.FindByID(r.Context(), actor.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	resp := MeResponse{
		Id: updated.ID, Login: updated.Login, Email: updated.Email,
		IsSuperAdmin: updated.IsSuperAdmin, MustChangePassword: updated.MustChangePassword,
	}
	if updated.AvatarSeed != "" {
		s := updated.AvatarSeed
		resp.AvatarSeed = &s
	}
	writeJSON(w, http.StatusOK, resp)
}
