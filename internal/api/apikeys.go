// Package api — user API key CRUD handlers (Phase 05-04).
//
// Endpoints under /api/v1/me/api-keys. All require session-or-API-key auth.
// The create response is the ONLY time the plaintext secret is returned
// (shown-once discipline, same pattern as s3_keys.go).
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// mountAPIKeys installs the /me/api-keys CRUD endpoints.
func (d Deps) mountAPIKeys(r chi.Router) {
	r.Route("/me/api-keys", func(r chi.Router) {
		r.Get("/", d.handleListAPIKeys)
		r.Post("/", d.handleCreateAPIKey)
		r.Delete("/{id}", d.handleRevokeAPIKey)
	})
}

// apiKeyCreateRequest is the POST body.
type apiKeyCreateRequest struct {
	Name string `json:"name"`
}

// apiKeyCreateResponse is the shown-once response. The Secret field is
// present ONLY here — never in apiKeyListItem.
type apiKeyCreateResponse struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Secret    string    `json:"secret"`
	CreatedAt time.Time `json:"created_at"`
}

// apiKeyListItem is the safe projection (no secret).
type apiKeyListItem struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func (d Deps) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	keys, err := d.APIKeys.ListByUser(r.Context(), actor.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	out := make([]apiKeyListItem, 0, len(keys))
	for _, k := range keys {
		out = append(out, apiKeyListItem{
			ID:         k.ID,
			Name:       k.Name,
			Prefix:     k.TokenPrefix,
			CreatedAt:  k.CreatedAt,
			LastUsedAt: k.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (d Deps) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	var req apiKeyCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "name required")
		return
	}

	key, err := auth.GenerateAPIKey(auth.APIKeyKindUser)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	id, err := d.APIKeys.CreateUserKey(r.Context(), actor.ID, req.Name, key.Prefix, key.SHA256)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	writeJSON(w, http.StatusCreated, apiKeyCreateResponse{
		ID:        id,
		Name:      req.Name,
		Prefix:    key.Prefix,
		Secret:    key.Plaintext,
		CreatedAt: d.clock(),
	})
}

func (d Deps) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid id")
		return
	}

	// Verify key belongs to this user.
	key, err := d.APIKeys.FindByID(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "api key not found")
		return
	}
	if key.OwnerKind != "user" || key.OwnerUserID == nil || *key.OwnerUserID != actor.ID {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "api key not found")
		return
	}

	if err := d.APIKeys.Revoke(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
