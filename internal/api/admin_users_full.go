// Package api — admin user CRUD (Phase 05-03, API-03/API-06).
//
// GET   /api/v1/admin/users           — paginated list of all users
// GET   /api/v1/admin/users/{login}   — full user detail with project memberships
// PATCH /api/v1/admin/users/{login}   — edit email, is_super_admin, force password reset
//
// Create and Delete already exist in admin_phase1.go; this file adds List, Get, Patch.
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// mountAdminUsersFull installs the extended user CRUD endpoints on r.
func (d Deps) mountAdminUsersFull(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionCreateUser)).
		Get("/admin/users", d.handleListUsers)
	r.With(authmw.RequireCan(auth.ActionCreateUser)).
		Get("/admin/users/{login}", d.handleGetUser)
	r.With(authmw.RequireCan(auth.ActionCreateUser)).
		Patch("/admin/users/{login}", d.handlePatchUser)
}

type userListItem struct {
	ID                 int64    `json:"id"`
	Login              string   `json:"login"`
	Email              string   `json:"email"`
	IsSuperAdmin       bool     `json:"is_super_admin"`
	MustChangePassword bool     `json:"must_change_password"`
	CreatedAt          string   `json:"created_at"`
	DeletedAt          *string  `json:"deleted_at,omitempty"`
	Projects           []string `json:"projects"`
}

func (d Deps) handleListUsers(w http.ResponseWriter, r *http.Request) {
	pp := ParsePaginationParams(r)
	// include_deleted=true surfaces soft-deleted users for the admin so the
	// UNIQUE(login) slot they still hold becomes visible and diagnosable
	// (F-7 admin half). Default stays false to match the existing contract
	// where listings show only live rows.
	includeDeleted := r.URL.Query().Get("include_deleted") == "true"

	var users []metadata.User
	var err error
	if includeDeleted {
		users, err = d.Users.ListAllIncludingDeleted(r.Context())
	} else {
		users, err = d.Users.ListAll(r.Context())
	}
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Apply cursor pagination (keyset on ID).
	startIdx := 0
	if pp.Cursor != nil {
		for i, u := range users {
			if u.ID == pp.Cursor.ID {
				startIdx = i + 1
				break
			}
		}
	}

	if startIdx >= len(users) {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "next_cursor": nil})
		return
	}

	end := startIdx + pp.Limit
	var nextCursor *string
	if end < len(users) {
		last := users[end-1]
		c := EncodeCursor(Cursor{ID: last.ID})
		nextCursor = &c
	} else {
		end = len(users)
	}

	items := make([]userListItem, 0, end-startIdx)
	for _, u := range users[startIdx:end] {
		// Fetch project memberships for each user.
		projIDs, _ := d.Members.ListProjectIDsForUser(r.Context(), u.ID)
		var projNames []string
		for _, pid := range projIDs {
			if p, err := d.Projects.FindByID(r.Context(), pid); err == nil {
				projNames = append(projNames, p.Name)
			}
		}
		if projNames == nil {
			projNames = []string{}
		}
		var deletedAt *string
		if u.DeletedAt != nil {
			s := u.DeletedAt.UTC().Format(time.RFC3339)
			deletedAt = &s
		}
		items = append(items, userListItem{
			ID:                 u.ID,
			Login:              u.Login,
			Email:              u.Email,
			IsSuperAdmin:       u.IsSuperAdmin,
			MustChangePassword: u.MustChangePassword,
			CreatedAt:          u.CreatedAt.UTC().Format(time.RFC3339),
			DeletedAt:          deletedAt,
			Projects:           projNames,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
	})
}

func (d Deps) handleGetUser(w http.ResponseWriter, r *http.Request) {
	login := chi.URLParam(r, "login")
	u, err := d.Users.FindByLogin(r.Context(), login)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "user not found")
		return
	}

	projIDs, _ := d.Members.ListProjectIDsForUser(r.Context(), u.ID)
	var projNames []string
	for _, pid := range projIDs {
		if p, err := d.Projects.FindByID(r.Context(), pid); err == nil {
			projNames = append(projNames, p.Name)
		}
	}
	if projNames == nil {
		projNames = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":                   u.ID,
		"login":                u.Login,
		"email":                u.Email,
		"avatar_seed":          u.AvatarSeed,
		"is_super_admin":       u.IsSuperAdmin,
		"must_change_password": u.MustChangePassword,
		"created_at":           u.CreatedAt.UTC().Format(time.RFC3339),
		"projects":             projNames,
	})
}

func (d Deps) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	login := chi.URLParam(r, "login")
	u, err := d.Users.FindByLogin(r.Context(), login)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "user not found")
		return
	}

	var patch struct {
		Email              *string `json:"email"`
		IsSuperAdmin       *bool   `json:"is_super_admin"`
		MustChangePassword *bool   `json:"must_change_password"`
		NewPassword        *string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&patch); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}

	// Diff-audit pattern: track what changed.
	changes := map[string]any{}

	if patch.Email != nil && *patch.Email != u.Email {
		if err := d.Users.UpdateEmail(r.Context(), u.ID, *patch.Email); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		changes["email"] = map[string]string{"from": u.Email, "to": *patch.Email}
	}

	if patch.IsSuperAdmin != nil && *patch.IsSuperAdmin != u.IsSuperAdmin {
		if err := d.Users.SetIsSuperAdmin(r.Context(), u.ID, *patch.IsSuperAdmin); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		changes["is_super_admin"] = *patch.IsSuperAdmin
	}

	if patch.MustChangePassword != nil && *patch.MustChangePassword != u.MustChangePassword {
		if err := d.Users.SetMustChangePassword(r.Context(), u.ID, *patch.MustChangePassword); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		changes["must_change_password"] = *patch.MustChangePassword
	}

	if patch.NewPassword != nil && *patch.NewPassword != "" {
		// wt4 F-04.2: admin force-reset MUST also enforce the password
		// floor — without this an admin could PATCH a user to "abc" via
		// the API even though setup/change-password reject it.
		if err := auth.PasswordValid(*patch.NewPassword); err != nil {
			writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
			return
		}
		hash, err := auth.HashPassword(*patch.NewPassword)
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		if err := d.Users.UpdatePasswordHash(r.Context(), u.ID, hash); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		if patch.MustChangePassword != nil && *patch.MustChangePassword {
			_ = d.Users.SetMustChangePassword(r.Context(), u.ID, true)
		}
		// HI-01: admin-forced reset invalidates every session for this user.
		// Unlike self-service change, there is no "preserve current session"
		// — an admin reset is precisely the scenario where we want every
		// cookie for the victim to die.
		_ = d.Sessions.DeleteAllForUser(r.Context(), u.ID)
		changes["password"] = "reset"
	}

	if len(changes) > 0 {
		if a, ok := auth.ActorFromContext(r.Context()); ok {
			uid := a.ID
			d.recordAudit(r, audit.Event{
				Kind:        audit.EvtUserUpdated,
				ActorUserID: &uid,
				TargetKind:  "user",
				TargetID:    login,
				Outcome:     "updated",
				Details:     changes,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "changes": changes})
}
