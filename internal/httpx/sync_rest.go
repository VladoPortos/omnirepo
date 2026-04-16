// Package httpx — Phase 03 Plan 06 SYNC-05 REST endpoint.
//
// Mounts POST /api/v1/projects/{name}/repos/{type}/{repo}/sync. The kind
// dispatch (rpm_sync vs apt_sync vs pypi_sync vs helm_sync) is derived
// from the resolved repo.type at enqueue time, so the SyncPool handler
// map registration in app.Run owns the per-protocol routing.
//
// To avoid an import cycle with internal/auth (which already imports
// internal/httpx for reserved-prefix validation), this file does not
// import internal/auth directly. Instead the wiring (internal/api)
// supplies an ActorResolver callback that pulls the actor identity off
// the request context — that callback is the only auth-aware piece.
package httpx

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// SyncActor is the auth-agnostic projection of the request actor used by
// the sync REST endpoint. Authenticated reports whether any actor is
// present (false → 401); UserID/APIKeyID feed the audit row.
type SyncActor struct {
	Authenticated bool
	UserID        int64 // 0 when not a user actor
	APIKeyID      int64 // 0 when not an API-key actor
	ProjectID     int64 // project scope for project-owned API keys
}

// SyncMembershipChecker reports whether actor (UserID) is a member of
// projectID. May be nil — when nil, membership is considered satisfied
// (the caller is expected to gate on something else, e.g. the wrapping
// middleware already required project-write).
type SyncMembershipChecker interface {
	IsMember(ctx interface{ Done() <-chan struct{} }, projectID, userID int64) (bool, error)
}

// SyncRESTDeps bundles dependencies for MountSyncRoutes.
type SyncRESTDeps struct {
	DB            *metadata.DB
	Repos         *metadata.ReposRepo
	Projects      *metadata.ProjectsRepo
	Members       *metadata.MembersRepo
	SyncJobs      *metadata.SyncJobsRepo
	UpstreamCreds *metadata.UpstreamCredsRepo
	Audit         audit.Logger
	Kick          func() // sync pool kick callback; may be nil in tests

	// ActorResolver pulls the auth actor off the request. Required.
	ActorResolver func(r *http.Request) SyncActor
}

// SyncRequest is the REST POST body shape.
type SyncRequest struct {
	UpstreamURL string          `json:"upstream_url"`
	CredID      *int64          `json:"cred_id,omitempty"`
	Filter      json.RawMessage `json:"filter,omitempty"` // protocol-specific opaque blob
	Suite       string          `json:"suite,omitempty"`  // deb only
}

// MountSyncRoutes wires the sync REST endpoint onto r. Caller is responsible
// for placing this inside an authenticated subtree (the handler re-checks
// project membership inline so 401/403/404 are distinct).
func MountSyncRoutes(r chi.Router, d SyncRESTDeps) {
	r.Post("/projects/{name}/repos/{type}/{repo}/sync", d.handleSync)
}

func (d SyncRESTDeps) handleSync(w http.ResponseWriter, r *http.Request) {
	if d.ActorResolver == nil {
		writeJSONErr(w, http.StatusInternalServerError, "internal", "actor resolver not wired")
		return
	}
	actor := d.ActorResolver(r)
	if !actor.Authenticated {
		writeJSONErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	if projectName == "" || repoType == "" || repoName == "" {
		writeJSONErr(w, http.StatusBadRequest, "validation_failed", "missing url params")
		return
	}
	switch repoType {
	case "rpm", "deb", "pypi", "helm":
	default:
		writeJSONErr(w, http.StatusBadRequest, "validation_failed", "unsupported repo type: "+repoType)
		return
	}

	proj, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil || proj == nil {
		writeJSONErr(w, http.StatusNotFound, "not_found", "project")
		return
	}
	repo, err := d.Repos.FindByTriple(r.Context(), proj.ID, repoType, repoName)
	if err != nil || repo == nil {
		writeJSONErr(w, http.StatusNotFound, "not_found", "repo")
		return
	}

	// Authorization: project member (or project-scoped API key) can write.
	if d.Members != nil {
		authorized := false
		if actor.UserID != 0 {
			isMember, mErr := d.Members.IsMember(r.Context(), proj.ID, actor.UserID)
			if mErr != nil {
				slog.ErrorContext(r.Context(), "sync.rest.member_check", "err", mErr)
				writeJSONErr(w, http.StatusInternalServerError, "internal", "")
				return
			}
			authorized = isMember
		} else if actor.APIKeyID != 0 && actor.ProjectID != 0 {
			authorized = actor.ProjectID == proj.ID
		}
		if !authorized {
			writeJSONErr(w, http.StatusForbidden, "forbidden", "not a project member")
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req SyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "validation_failed", "invalid JSON: "+err.Error())
		return
	}
	if req.UpstreamURL == "" {
		writeJSONErr(w, http.StatusBadRequest, "validation_failed", "upstream_url required")
		return
	}
	u, perr := url.Parse(req.UpstreamURL)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeJSONErr(w, http.StatusBadRequest, "validation_failed", "upstream_url must be http(s)")
		return
	}

	// Synchronous host-match validation when cred_id provided.
	if req.CredID != nil && d.UpstreamCreds != nil {
		_, _, _, credHost, lerr := d.UpstreamCreds.Lookup(r.Context(), proj.ID, *req.CredID)
		if lerr != nil {
			if errors.Is(lerr, metadata.ErrNotFound) || errors.Is(lerr, metadata.ErrForeignProject) {
				writeJSONErr(w, http.StatusNotFound, "not_found", "cred")
				return
			}
			slog.ErrorContext(r.Context(), "sync.rest.cred_lookup", "err", lerr)
			writeJSONErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
		if credHost != u.Host {
			writeJSONErr(w, http.StatusBadRequest, "cred_host_mismatch",
				fmt.Sprintf("cred host %q != upstream host %q", credHost, u.Host))
			return
		}
	}

	// Build payload — protocol-specific, but we serialize the same shape
	// (the per-protocol Handle re-unmarshals into its own struct; extra
	// fields are ignored by the JSON decoder).
	payload := map[string]any{
		"upstream_url": req.UpstreamURL,
	}
	if req.CredID != nil {
		payload["cred_id"] = *req.CredID
	}
	if len(req.Filter) > 0 {
		payload["filter"] = json.RawMessage(req.Filter)
	}
	if repoType == "deb" && req.Suite != "" {
		payload["suite"] = req.Suite
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "internal", "marshal payload")
		return
	}

	kind := repoType + "_sync"
	if repoType == "deb" {
		kind = "apt_sync"
	}

	var jobID int64
	err = d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		id, err := d.SyncJobs.Enqueue(r.Context(), tx, kind, proj.ID, repo.ID, string(buf))
		if err != nil {
			return err
		}
		jobID = id
		return nil
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "sync.rest.enqueue", "err", err)
		writeJSONErr(w, http.StatusInternalServerError, "internal", "enqueue")
		return
	}
	if d.Kick != nil {
		d.Kick()
	}
	if d.Audit != nil {
		ev := audit.Event{
			Kind:       audit.EvtSyncStarted,
			TargetKind: "repo",
			TargetID:   fmt.Sprintf("%s/%s/%s", projectName, repoType, repoName),
			Details: map[string]any{
				"upstream_url": req.UpstreamURL,
				"job_id":       jobID,
				"job_kind":     kind,
				"cred_id":      req.CredID,
			},
		}
		if actor.UserID != 0 {
			id := actor.UserID
			ev.ActorUserID = &id
		} else if actor.APIKeyID != 0 {
			id := actor.APIKeyID
			ev.ActorAPIKeyID = &id
		}
		_ = d.Audit.Record(r.Context(), ev)
	}

	writeJSONOK(w, http.StatusAccepted, map[string]any{"job_id": jobID, "kind": kind})
}

func writeJSONErr(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": code, "detail": detail})
}

func writeJSONOK(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
