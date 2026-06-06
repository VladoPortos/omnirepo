// Package api — upstream credential CRUD handlers.
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

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/httperr"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// debCredKindMigrationEnvelope returns the 400 envelope used by both the
// POST and PATCH pre-checks when a client submits kind="deb". The
// ApiErrorEnvelope contract is the canonical shape (top-level
// code/message/class/details) — not wrapped under a `.error` key — per
// web/src/api/client.ts:33-40 and errors_bridge_test.go. `details.received`
// + `details.accepted` ride on the `[key: string]: unknown` index signature
// in ApiErrorDetails, so no TS widening is required on the client side.
//
// Code is the dotted form `validation.invalid_cred_kind` so it matches the
// OpenAPI envelope regex `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`. This is a
// stricter choice than the `invalid_cred_kind` alternative; the UI regex
// check `isApiErrorEnvelope` doesn't enforce it, but the OpenAPI schema
// does, and tests downstream of the schema would fail validation.
//
// Status is deliberately 400, NOT the 422 that the generic
// ValidCredKinds fallback emits: 400 = "you sent a recognised-but-retired
// value, here is a migration hint"; 422 = "you sent a value the server
// has never heard of".
func debCredKindMigrationEnvelope() *httperr.Error {
	return &httperr.Error{
		Envelope: httperr.Envelope{
			Code:    "validation.invalid_cred_kind",
			Message: `kind "deb" is not accepted; use "apt"`,
			Class:   httperr.ClassValidation,
			Details: map[string]any{
				"field":    "kind",
				"received": "deb",
				"accepted": []string{"docker", "rpm", "apt", "pypi", "helm"},
			},
		},
		Status: http.StatusBadRequest,
	}
}

// maxUpstreamCredsBodyBytes caps request bodies to 64 KiB.
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

func parseCredID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid id")
		return 0, false
	}
	return id, true
}

func (d Deps) handleListUpstreamCreds(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveProjectForAction(w, r, auth.ActionManageUpstreamCreds)
	if !ok {
		return
	}
	metas, err := d.UpstreamCreds.List(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	out := make([]upstreamCredResponse, 0, len(metas))
	for _, m := range metas {
		out = append(out, credMetaToResponse(m))
	}
	writeJSON(w, http.StatusOK, out)
}

func (d Deps) handleGetUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveProjectForAction(w, r, auth.ActionManageUpstreamCreds)
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
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "upstream cred not found")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, credMetaToResponse(*m))
}

func (d Deps) handleCreateUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectForAction(w, r, auth.ActionManageUpstreamCreds)
	if !ok {
		return
	}
	var req upstreamCredCreateRequest
	if err := decodeCredBody(r, &req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, err.Error())
		return
	}
	if req.Host == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "host required")
		return
	}
	// Reject legacy `kind="deb"` submissions with a machine-readable
	// migration hint BEFORE the generic ValidCredKinds lookup. See
	// debCredKindMigrationEnvelope for the full wire shape.
	if req.Kind == "deb" {
		writeEnvelope(w, r, debCredKindMigrationEnvelope())
		return
	}
	kind := metadata.CredKind(req.Kind)
	if _, ok := metadata.ValidCredKinds[kind]; !ok {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "invalid kind")
		return
	}

	id, err := d.UpstreamCreds.Create(r.Context(), projectID, req.Host, kind,
		req.Username, req.Password, req.Token, actor.ID)
	switch {
	case errors.Is(err, metadata.ErrSecretRequired):
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "password_or_token_required")
		return
	case err != nil:
		// UNIQUE(project_id, host, kind) is reported as a driver error; surface
		// as 409 via string-match (same pattern as handleCreateProject).
		if isUniqueConstraintErr(err) {
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "upstream cred already exists for host+kind")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Load back for the response (so we return the DB-generated timestamps).
	m, err := d.UpstreamCreds.Get(r.Context(), projectID, id)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtUpstreamCredCreated,
		TargetKind: "upstream_cred",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"host":    req.Host,
			"kind":    req.Kind,
			"project": projectName,
			"id":      id,
		},
	}, actor)
	writeJSON(w, http.StatusCreated, credMetaToResponse(*m))
}

func (d Deps) handleUpdateUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectForAction(w, r, auth.ActionManageUpstreamCreds)
	if !ok {
		return
	}
	id, ok := parseCredID(w, r)
	if !ok {
		return
	}
	var req upstreamCredCreateRequest
	if err := decodeCredBody(r, &req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, err.Error())
		return
	}
	// Reject legacy `kind="deb"` on PATCH too, even though Update() doesn't
	// persist the kind field — an upgraded client whose cached form state
	// still carries "deb" gets the same 400 migration hint as on POST. This
	// keeps the contract uniform across POST + PATCH. See
	// debCredKindMigrationEnvelope.
	if req.Kind == "deb" {
		writeEnvelope(w, r, debCredKindMigrationEnvelope())
		return
	}
	if err := d.UpstreamCreds.Update(r.Context(), projectID, id, req.Username, req.Password, req.Token); err != nil {
		switch {
		case errors.Is(err, metadata.ErrSecretRequired):
			writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "password_or_token_required")
		case errors.Is(err, metadata.ErrNotFound), errors.Is(err, metadata.ErrForeignProject):
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "upstream cred not found")
		default:
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		}
		return
	}
	m, err := d.UpstreamCreds.Get(r.Context(), projectID, id)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtUpstreamCredUpdated,
		TargetKind: "upstream_cred",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"host":    m.Host,
			"kind":    string(m.Kind),
			"project": projectName,
			"id":      id,
		},
	}, actor)
	writeJSON(w, http.StatusOK, credMetaToResponse(*m))
}

func (d Deps) handleDeleteUpstreamCred(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectForAction(w, r, auth.ActionManageUpstreamCreds)
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
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "upstream cred not found")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if err := d.UpstreamCreds.Delete(r.Context(), projectID, id); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtUpstreamCredDeleted,
		TargetKind: "upstream_cred",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"host":    m.Host,
			"kind":    string(m.Kind),
			"project": projectName,
			"id":      id,
		},
	}, actor)
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
