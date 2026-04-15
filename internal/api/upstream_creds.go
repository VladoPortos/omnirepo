// Package api — upstream credential CRUD handlers (Phase 02-02, D-11/D-13).
//
// Endpoints live under /api/v1/projects/{name}/upstream-creds. All require
// session-or-API-key auth AND project-member authorization via
// auth.ActionManageUpstreamCreds. Response shapes enumerate
// {id, host, kind, username, created_at, updated_at} — secrets NEVER appear
// in a response body. Every state-changing call emits an audit event whose
// Details map carries only {host, kind, project, id} — never plaintext.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// maxUpstreamCredsBodyBytes caps request bodies to 64 KiB (T-02-02-06).
const maxUpstreamCredsBodyBytes = 64 * 1024

// upstreamCredCreateRequest is the POST/PATCH body shape. password and token
// are write-only; responses never echo either.
type upstreamCredCreateRequest struct {
	Host     string `json:"host,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

// upstreamCredResponse is the secret-free projection returned by every
// endpoint. If you add a field here, double-check it does not carry a secret.
type upstreamCredResponse struct {
	ID        int64     `json:"id"`
	Host      string    `json:"host"`
	Kind      string    `json:"kind"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func credMetaToResponse(m metadata.CredMeta) upstreamCredResponse {
	return upstreamCredResponse{
		ID:        m.ID,
		Host:      m.Host,
		Kind:      string(m.Kind),
		Username:  m.Username,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// mountUpstreamCreds installs the upstream-cred CRUD endpoints under the
// already-auth'd /api/v1 subrouter. Caller must supply a non-nil
// UpstreamCreds repo; if it is nil (AEAD not materialized) the routes are
// skipped.
func (d Deps) mountUpstreamCreds(r chi.Router) {
	if d.UpstreamCreds == nil {
		return
	}
	r.Route("/projects/{name}/upstream-creds", func(r chi.Router) {
		r.Get("/", d.handleListUpstreamCreds)
		r.Post("/", d.handleCreateUpstreamCred)
		r.Get("/{id}", d.handleGetUpstreamCred)
		r.Patch("/{id}", d.handleUpdateUpstreamCred)
		r.Delete("/{id}", d.handleDeleteUpstreamCred)
	})
}

// resolveProjectAndCheckMembership handles 401/403/404 for every upstream-cred
// handler and returns the project id on success.
func (d Deps) resolveProjectAndCheckMembership(w http.ResponseWriter, r *http.Request) (int64, string, auth.Actor, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return 0, "", auth.Actor{}, false
	}
	projectName := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return 0, "", auth.Actor{}, false
	}
	if allowed, reason := auth.Can(r.Context(), actor, auth.ActionManageUpstreamCreds,
		auth.Target{Kind: "project", ProjectID: p.ID}); !allowed {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, reason)
		return 0, "", auth.Actor{}, false
	}
	return p.ID, p.Name, actor, true
}

func parseCredID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid id")
		return 0, false
	}
	return id, true
}

func (d Deps) handleListUpstreamCreds(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveProjectAndCheckMembership(w, r)
	if !ok {
		return
	}
	metas, err := d.UpstreamCreds.List(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	out := make([]upstreamCredResponse, 0, len(metas))
	for _, m := range metas {
		out = append(out, credMetaToResponse(m))
	}
	writeJSON(w, http.StatusOK, out)
}

func (d Deps) handleGetUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveProjectAndCheckMembership(w, r)
	if !ok {
		return
	}
	id, ok := parseCredID(w, r)
	if !ok {
		return
	}
	m, err := d.UpstreamCreds.Get(r.Context(), projectID, id)
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) || errors.Is(err, metadata.ErrForeignProject) {
			writeJSONError(w, http.StatusNotFound, ErrNotFound, "upstream cred not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, credMetaToResponse(*m))
}

func (d Deps) handleCreateUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectAndCheckMembership(w, r)
	if !ok {
		return
	}
	var req upstreamCredCreateRequest
	if err := decodeCredBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, err.Error())
		return
	}
	if req.Host == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "host required")
		return
	}
	kind := metadata.CredKind(req.Kind)
	if _, ok := metadata.ValidCredKinds[kind]; !ok {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "invalid kind")
		return
	}

	id, err := d.UpstreamCreds.Create(r.Context(), projectID, req.Host, kind,
		req.Username, req.Password, req.Token, actor.ID)
	switch {
	case errors.Is(err, metadata.ErrSecretRequired):
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "password_or_token_required")
		return
	case err != nil:
		// UNIQUE(project_id, host, kind) is reported as a driver error; surface
		// as 409 via string-match (same pattern as handleCreateProject).
		if isUniqueConstraintErr(err) {
			writeJSONError(w, http.StatusConflict, ErrConflict, "upstream cred already exists for host+kind")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Load back for the response (so we return the DB-generated timestamps).
	m, err := d.UpstreamCreds.Get(r.Context(), projectID, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	uid := actor.ID
	d.recordAudit(r, audit.Event{
		Kind:       audit.EvtUpstreamCredCreated,
		ActorUserID: &uid,
		TargetKind: "upstream_cred",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"host":    req.Host,
			"kind":    req.Kind,
			"project": projectName,
			"id":      id,
		},
	})
	writeJSON(w, http.StatusCreated, credMetaToResponse(*m))
}

func (d Deps) handleUpdateUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectAndCheckMembership(w, r)
	if !ok {
		return
	}
	id, ok := parseCredID(w, r)
	if !ok {
		return
	}
	var req upstreamCredCreateRequest
	if err := decodeCredBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, err.Error())
		return
	}
	if err := d.UpstreamCreds.Update(r.Context(), projectID, id, req.Username, req.Password, req.Token); err != nil {
		switch {
		case errors.Is(err, metadata.ErrSecretRequired):
			writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "password_or_token_required")
		case errors.Is(err, metadata.ErrNotFound), errors.Is(err, metadata.ErrForeignProject):
			writeJSONError(w, http.StatusNotFound, ErrNotFound, "upstream cred not found")
		default:
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		}
		return
	}
	m, err := d.UpstreamCreds.Get(r.Context(), projectID, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	uid := actor.ID
	d.recordAudit(r, audit.Event{
		Kind:       audit.EvtUpstreamCredUpdated,
		ActorUserID: &uid,
		TargetKind: "upstream_cred",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"host":    m.Host,
			"kind":    string(m.Kind),
			"project": projectName,
			"id":      id,
		},
	})
	writeJSON(w, http.StatusOK, credMetaToResponse(*m))
}

func (d Deps) handleDeleteUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectAndCheckMembership(w, r)
	if !ok {
		return
	}
	id, ok := parseCredID(w, r)
	if !ok {
		return
	}
	// Fetch meta for the audit detail before delete.
	m, err := d.UpstreamCreds.Get(r.Context(), projectID, id)
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) || errors.Is(err, metadata.ErrForeignProject) {
			writeJSONError(w, http.StatusNotFound, ErrNotFound, "upstream cred not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if err := d.UpstreamCreds.Delete(r.Context(), projectID, id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	uid := actor.ID
	d.recordAudit(r, audit.Event{
		Kind:       audit.EvtUpstreamCredDeleted,
		ActorUserID: &uid,
		TargetKind: "upstream_cred",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"host":    m.Host,
			"kind":    string(m.Kind),
			"project": projectName,
			"id":      id,
		},
	})
	w.WriteHeader(http.StatusNoContent)
}

// decodeCredBody decodes the JSON request body with a 64 KiB cap.
func decodeCredBody(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxUpstreamCredsBodyBytes)
	dec := json.NewDecoder(r.Body)
	return dec.Decode(dst)
}

// isUniqueConstraintErr returns true when err is a SQLite UNIQUE constraint
// violation. We string-match because modernc/sqlite does not expose a typed
// sentinel; same pattern as the Project/Repo create handlers.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return contains(s, "UNIQUE") || contains(s, "constraint failed")
}

func contains(s, sub string) bool {
	// tiny wrapper to avoid importing strings only for Contains in this file;
	// strings is already imported by other api files but we prefer minimal
	// dependency surface here.
	n := len(sub)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return true
		}
	}
	return false
}
