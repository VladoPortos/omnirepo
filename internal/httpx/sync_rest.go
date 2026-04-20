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
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// MaxSyncBodyBytes caps POST /sync request bodies (Phase 8 Plan 01,
// MIRROR-06). Sync payloads carry only filter JSON + metadata — 16 KiB is
// generous for the largest realistic allowlist. The io.LimitReader uses
// MaxSyncBodyBytes+1 so the handler can detect "exactly at or over limit"
// without allocating past the cap.
const MaxSyncBodyBytes = 16 * 1024

// Phase 8 Plan 01 canonical sync envelope codes. Dotted wire form
// satisfies the envelope schema regex while the second segment keeps the
// operator-facing token verbatim so grep-based plan-check assertions
// resolve against this file.
const (
	codeSyncMirrorOverridesNotAllowed = "sync.mirror_overrides_not_allowed"
	codeSyncAlreadyRunning            = "sync.sync_already_running"
	codeSyncInvalidRequestBody        = "sync.invalid_request_body"
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

	// Phase 8 Plan 01 (MIRROR-06): cap body at 16 KiB with io.LimitReader.
	// The +1 over-by-one trick lets us detect "at or over limit" without
	// allocating past the cap. Explicit ReadAll (rather than decoder +
	// MaxBytesReader) so we can inspect body length BEFORE JSON parsing
	// for the mirror-overrides-not-allowed branch below.
	bodyBytes, readErr := io.ReadAll(io.LimitReader(r.Body, MaxSyncBodyBytes+1))
	_ = r.Body.Close()
	if readErr != nil {
		writeJSONErr(w, http.StatusBadRequest, codeSyncInvalidRequestBody, "request body read failed")
		return
	}
	if int64(len(bodyBytes)) > MaxSyncBodyBytes {
		writeJSONErr(w, http.StatusBadRequest, codeSyncInvalidRequestBody, "request body exceeds 16 KiB limit")
		return
	}
	bodyEmpty := len(bytes.TrimSpace(bodyBytes)) == 0

	// Phase 8 Plan 01 (MIRROR-05): mirror repos read config from the repo
	// row; a non-empty body would risk operators accidentally overriding the
	// locked-at-creation filter/URL. Reject with 400 before anything else.
	if repo.IsMirror && !bodyEmpty {
		writeJSONErr(w, http.StatusBadRequest, codeSyncMirrorOverridesNotAllowed,
			"mirror repos read config from the repo row; do not send a body")
		return
	}

	// Phase 8 Plan 01 (MIRROR-04): one-in-flight-sync-per-repo. The
	// CountRepoInflight + subsequent Enqueue are NOT atomic; a race between
	// two concurrent POSTs can produce two pending rows. The worker pool's
	// LeaseOne uses UPDATE ... RETURNING so only one lease wins, capping
	// the residual cost at one wasted pending row (T-08-01-04).
	inflight, cerr := d.SyncJobs.CountRepoInflight(r.Context(), repo.ID)
	if cerr != nil {
		slog.ErrorContext(r.Context(), "sync.rest.inflight_count", "err", cerr)
		writeJSONErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if inflight > 0 {
		writeJSONErr(w, http.StatusConflict, codeSyncAlreadyRunning,
			"a sync is already running for this repo")
		return
	}

	// Branch on IsMirror to source the sync payload.
	var req SyncRequest
	if repo.IsMirror {
		// bodyEmpty asserted above; read config from the repo row.
		req.UpstreamURL = repo.MirrorUpstreamURL
		if repo.MirrorCredID != nil {
			v := *repo.MirrorCredID
			req.CredID = &v
		}
		if repo.MirrorFilterJSON != "" {
			req.Filter = json.RawMessage(repo.MirrorFilterJSON)
		}
	} else {
		// Non-mirror repos keep the v1.0 body-driven flow verbatim.
		if !bodyEmpty {
			if err := json.Unmarshal(bodyBytes, &req); err != nil {
				writeJSONErr(w, http.StatusBadRequest, codeSyncInvalidRequestBody, "invalid JSON: "+err.Error())
				return
			}
		}
		if req.UpstreamURL == "" {
			writeJSONErr(w, http.StatusBadRequest, "validation_failed", "upstream_url required")
			return
		}
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
	buf, merr := json.Marshal(payload)
	if merr != nil {
		writeJSONErr(w, http.StatusInternalServerError, "internal", "marshal payload")
		return
	}

	kind := repoType + "_sync"
	if repoType == "deb" {
		kind = "apt_sync"
	}

	var jobID int64
	wtxErr := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		id, err := d.SyncJobs.Enqueue(r.Context(), tx, kind, proj.ID, repo.ID, string(buf))
		if err != nil {
			return err
		}
		jobID = id
		return nil
	})
	if wtxErr != nil {
		slog.ErrorContext(r.Context(), "sync.rest.enqueue", "err", wtxErr)
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
