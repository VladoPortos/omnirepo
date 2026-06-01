// Package api — project-scoped API key CRUD handlers.
//
// Endpoints under /api/v1/projects/{name}/api-keys. All require
// session-or-API-key auth AND project-member authorization via
// auth.ActionManageS3Keys (same membership semantics — manage =
// project-member). The create response is the ONLY time the plaintext
// "omr_p_*" secret is returned (shown-once discipline, mirrors
// s3_keys.go).
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
)

// projectAPIKeyCreateRequest is the POST body.
type projectAPIKeyCreateRequest struct {
	Name string `json:"name"`
	Role string `json:"role"` // "maintainer"|"viewer"; defaults to "maintainer"
}

// projectAPIKeyCreateResponse is the shown-once response. Secret is
// present ONLY here.
type projectAPIKeyCreateResponse struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Secret    string    `json:"secret"`
	CreatedAt time.Time `json:"created_at"`
}

// projectAPIKeyListItem is the safe projection (no secret).
type projectAPIKeyListItem struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// mountProjectAPIKeys installs the /projects/{name}/api-keys CRUD
// endpoints under the already-auth'd /api/v1 subrouter.
func (d Deps) mountProjectAPIKeys(r chi.Router) {
	r.Route("/projects/{name}/api-keys", func(r chi.Router) {
		r.Get("/", d.handleListProjectAPIKeys)
		r.Post("/", d.handleCreateProjectAPIKey)
		r.Delete("/{id}", d.handleRevokeProjectAPIKey)
	})
}

// resolveProjectAndCheckAPIKeysMembership handles 401/403/404 and
// returns the project id + name + actor on success.
func (d Deps) resolveProjectAndCheckAPIKeysMembership(w http.ResponseWriter, r *http.Request) (int64, string, auth.Actor, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return 0, "", auth.Actor{}, false
	}
	projectName := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return 0, "", auth.Actor{}, false
	}
	if allowed, reason := auth.Can(r.Context(), actor, auth.ActionManageProjectAPIKeys,
		auth.Target{Kind: "project", ProjectID: p.ID}); !allowed {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, reason)
		return 0, "", auth.Actor{}, false
	}
	return p.ID, p.Name, actor, true
}

func (d Deps) handleListProjectAPIKeys(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveProjectAndCheckAPIKeysMembership(w, r)
	if !ok {
		return
	}
	keys, err := d.APIKeys.ListByProject(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	out := make([]projectAPIKeyListItem, 0, len(keys))
	for _, k := range keys {
		out = append(out, projectAPIKeyListItem{
			ID:         k.ID,
			Name:       k.Name,
			Prefix:     k.TokenPrefix,
			CreatedAt:  k.CreatedAt,
			LastUsedAt: k.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (d Deps) handleCreateProjectAPIKey(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectAndCheckAPIKeysMembership(w, r)
	if !ok {
		return
	}
	var req projectAPIKeyCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "name required")
		return
	}
	if len(name) > maxAPIKeyNameLen {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "name too long")
		return
	}

	// Project-owned keys carry an explicit role. Default to "maintainer"
	// (CI-publish friendliness); callers pass "viewer" for read-only
	// scraper tokens.
	role := req.Role
	if role == "" {
		role = "maintainer"
	}
	if role != "maintainer" && role != "viewer" {
		writeFieldValidationError(w, r, ErrValidationFailed, "role", "must be 'maintainer' or 'viewer'")
		return
	}
	// Same duplicate-name guard as the user-scoped twin: names are the
	// primary way pipelines are identified in the UI table.
	existing, err := d.APIKeys.ListByProject(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	for _, k := range existing {
		if k.Name == name {
			writeJSONError(w, r, http.StatusConflict, ErrValidationFailed, "name already in use")
			return
		}
	}

	key, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	id, err := d.APIKeys.CreateProjectKeyWithRole(r.Context(), projectID, name, key.Prefix, key.SHA256, role)
	if err != nil {
		// Partial unique index idx_apikeys_project_live_name
		// (migration 028) backstops the racy app-layer check above.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeJSONError(w, r, http.StatusConflict, ErrValidationFailed, "name already in use")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtProjectAPIKeyCreated,
		TargetKind: "project_api_key",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"project": projectName,
			"id":      id,
			"name":    name,
			"prefix":  key.Prefix,
		},
	}, actor)

	writeJSON(w, http.StatusCreated, projectAPIKeyCreateResponse{
		ID:        id,
		Name:      name,
		Prefix:    key.Prefix,
		Secret:    key.Plaintext,
		CreatedAt: d.clock(),
	})
}

func (d Deps) handleRevokeProjectAPIKey(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectAndCheckAPIKeysMembership(w, r)
	if !ok {
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid id")
		return
	}

	// Verify the key exists and belongs to this project (don't leak
	// other projects' keys via cross-project revoke).
	key, err := d.APIKeys.FindByID(r.Context(), id)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "api key not found")
		return
	}
	if key.OwnerKind != "project" || key.OwnerProjectID == nil || *key.OwnerProjectID != projectID {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "api key not found")
		return
	}

	if err := d.APIKeys.Revoke(r.Context(), id); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtProjectAPIKeyRevoked,
		TargetKind: "project_api_key",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"project": projectName,
			"id":      id,
			"name":    key.Name,
		},
	}, actor)

	w.WriteHeader(http.StatusNoContent)
}
