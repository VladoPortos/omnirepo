// Package httpx — sync REST endpoint.
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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/httperr"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// MaxSyncBodyBytes caps POST /sync request bodies. Sync payloads carry only
// filter JSON + metadata — 16 KiB is generous for the largest realistic
// allowlist. The io.LimitReader uses MaxSyncBodyBytes+1 so the handler can
// detect "exactly at or over limit" without allocating past the cap.
const MaxSyncBodyBytes = 16 * 1024

// Canonical sync envelope codes. Dotted wire form satisfies the envelope
// schema regex while the second segment keeps the operator-facing token
// verbatim.
//
// The concurrent-sync-collision 409 code is generalized to
// `codeMirrorSyncInFlight` (below) so it can carry cross-kind details
// `{kind, job_id, started_at}` under a single envelope. This is a clean
// switch (no external API stability promise in v1.x point releases). Docs
// and cron scripts in `docs/operations/scheduled-sync.md` track the code.
const (
	codeSyncMirrorOverridesNotAllowed = "sync.mirror_overrides_not_allowed"
	codeSyncInvalidRequestBody        = "sync.invalid_request_body"

	// codeMirrorSyncInFlight is the generalized 409 envelope code
	// for a concurrent-sync collision on the same repo. Emitted via
	// httperr.Write with details={kind, job_id, started_at} so the UI
	// renders "Sync already running — started N min ago" uniformly
	// across protocol kinds (apt_sync, rpm_sync, pypi_sync, helm_sync,
	// git_sync).
	codeMirrorSyncInFlight = "mirror.sync.in_flight"
)

// SyncActor is the auth-agnostic projection of the request actor used by
// the sync REST endpoint. Authenticated reports whether any actor is
// present (false → 401); UserID/APIKeyID feed the audit row.
// IsSuperAdmin mirrors auth.Actor.IsSuperAdmin so this handler honors the
// same super-admin bypass the central auth.Can policy applies; without it,
// super-admins creating a mirror repo via the middleware-gated path get a
// 403 from this endpoint when they try to sync the same repo.
type SyncActor struct {
	Authenticated bool
	UserID        int64 // 0 when not a user actor
	APIKeyID      int64 // 0 when not an API-key actor
	ProjectID     int64 // project scope for project-owned API keys
	IsSuperAdmin  bool
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
	// ForceDriftThreshold is the operator-confirmed
	// override for the percent-threshold drift-purge guard. Mirror
	// repos accept this field even though they reject other
	// override fields (sync.mirror_overrides_not_allowed) — it is
	// not an "override" in the lock-at-creation sense; it's a
	// per-call confirmation that the operator has reviewed the
	// blocked-purge banner and wants the next sync to proceed
	// despite the guard. Threaded into the per-protocol payload
	// JSON as `force_drift_threshold` so each sync handler picks
	// it up via SyncPayload.ForceDriftThreshold.
	ForceDriftThreshold bool `json:"force_drift_threshold,omitempty"`
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
	case "rpm", "deb", "pypi", "helm", "git":
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
	// Super-admins bypass membership per auth.Can's rule; without
	// this bypass a super-admin who created a mirror repo via the
	// middleware-gated path gets a 403 trying to sync it.
	if d.Members != nil {
		authorized := actor.IsSuperAdmin
		if !authorized && actor.UserID != 0 {
			isMember, mErr := d.Members.IsMember(r.Context(), proj.ID, actor.UserID)
			if mErr != nil {
				slog.ErrorContext(r.Context(), "sync.rest.member_check", "err", mErr)
				writeJSONErr(w, http.StatusInternalServerError, "internal", "")
				return
			}
			authorized = isMember
		} else if !authorized && actor.APIKeyID != 0 && actor.ProjectID != 0 {
			authorized = actor.ProjectID == proj.ID
		}
		if !authorized {
			writeJSONErr(w, http.StatusForbidden, "forbidden", "not a project member")
			return
		}
	}

	// Cap body at 16 KiB with io.LimitReader.
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

	// Mirror repos read config from the repo row; a non-empty body would
	// risk operators accidentally overriding the locked-at-creation
	// filter/URL. Reject with 400 before anything else.
	//
	// Narrow exception: a body whose ONLY non-zero field
	// is `force_drift_threshold` is allowed for mirror repos — it's a
	// per-call confirmation flag, not an override of the locked config.
	// Any other field present (upstream_url/cred_id/filter/suite) keeps
	// the strict rejection path.
	var mirrorForceDriftThreshold bool
	if repo.IsMirror && !bodyEmpty {
		var probe SyncRequest
		if perr := json.Unmarshal(bodyBytes, &probe); perr != nil {
			writeJSONErr(w, http.StatusBadRequest, codeSyncInvalidRequestBody,
				"invalid JSON: "+perr.Error())
			return
		}
		hasOverrides := probe.UpstreamURL != "" || probe.CredID != nil ||
			len(probe.Filter) > 0 || probe.Suite != ""
		if hasOverrides {
			writeJSONErr(w, http.StatusBadRequest, codeSyncMirrorOverridesNotAllowed,
				"mirror repos read config from the repo row; do not send a body")
			return
		}
		mirrorForceDriftThreshold = probe.ForceDriftThreshold
	}

	// One-in-flight-sync-per-repo.
	// The fast-path pre-check uses GetInflightTx
	// via the reader pool equivalent — but since GetInflightTx is
	// writer-tx-scoped for the race-closing guarantee below, the fast-path
	// here calls CountRepoInflight (reader pool) and, on a hit, does a
	// second read through GetInflight to populate the envelope details.
	// The authoritative check still runs inside the writer tx further
	// down, so this duplicate is an optimization, not a correctness gate.
	inflight, cerr := d.SyncJobs.CountRepoInflight(r.Context(), repo.ID)
	if cerr != nil {
		slog.ErrorContext(r.Context(), "sync.rest.inflight_count", "err", cerr)
		writeJSONErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if inflight > 0 {
		writeMirrorSyncInFlight(w, r, d, repo.ID)
		return
	}

	// Branch on IsMirror to source the sync payload.
	var req SyncRequest
	if repo.IsMirror {
		// Mirror config comes from the repo row; only the
		// force_drift_threshold confirmation flag (if any) flows in
		// from the request body.
		req.UpstreamURL = repo.MirrorUpstreamURL
		if repo.MirrorCredID != nil {
			v := *repo.MirrorCredID
			req.CredID = &v
		}
		if repo.MirrorFilterJSON != "" {
			req.Filter = json.RawMessage(repo.MirrorFilterJSON)
		}
		req.ForceDriftThreshold = mirrorForceDriftThreshold
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
	// Helm mirrors widen to oci:// in validateMirrorUpstreamURL;
	// the sync-trigger endpoint must accept the same set of schemes or a
	// created mirror can never actually sync. Other protocols keep the
	// http(s)-only contract.
	//
	// Gate OCI on repo.IsMirror so a non-mirror helm repo's body-driven
	// sync (pull-external-style) can't smuggle in an oci:// URL that was
	// never vetted by
	// validateMirrorUpstreamURL + refuseDockerHubWithoutCred. Also require
	// a non-empty path for oci:// so "oci://host" (classifyHelmUpstream
	// rejects it at create-time) stays rejected here if ever reached.
	allowOCI := repoType == "helm" && repo.IsMirror
	schemeOK := u.Scheme == "http" || u.Scheme == "https" ||
		(allowOCI && u.Scheme == "oci" && u.Path != "" && u.Path != "/")
	if perr != nil || u.Host == "" || !schemeOK {
		msg := "upstream_url must be http(s)"
		if allowOCI {
			msg = "upstream_url must be http(s) or oci://host/path (helm only)"
		}
		writeJSONErr(w, http.StatusBadRequest, "validation_failed", msg)
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
	// Thread the operator-confirm flag into the
	// per-protocol payload. Each handler's SyncPayload picks it up
	// via the `force_drift_threshold` JSON tag and forwards it to
	// driftpurge.Run as the `force` parameter.
	if req.ForceDriftThreshold {
		payload["force_drift_threshold"] = true
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

	// Inflight-check + Enqueue must be atomic against concurrent /sync
	// POSTs. The earlier Reader-pool CountRepoInflight above is a
	// fast-path short-circuit; the authoritative check lives inside the
	// writer tx. SQLite serialises writer-pool statements so the
	// tx-scoped check+insert pair is race-free — a second caller either
	// (a) sees the first tx's pending row and returns inflightErr with
	// populated details, or (b) commits first and makes the second caller
	// observe the in-flight row.
	//
	// The tx-scoped check uses GetInflightTx so the returned details
	// (kind, job_id, started_at) flow into the 409 envelope without a
	// second read.
	var jobID int64
	wtxErr := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		got, exists, err := d.SyncJobs.GetInflightTx(r.Context(), tx, repo.ID)
		if err != nil {
			return err
		}
		if exists {
			return &inflightErr{job: got}
		}
		id, err := d.SyncJobs.Enqueue(r.Context(), tx, kind, proj.ID, repo.ID, string(buf))
		if err != nil {
			return err
		}
		jobID = id
		return nil
	})
	var iErr *inflightErr
	if errors.As(wtxErr, &iErr) {
		httperr.Write(w, r, &httperr.Error{
			Envelope: httperr.Envelope{
				Code:    codeMirrorSyncInFlight,
				Message: "A sync is already running for this repo.",
				Class:   httperr.ClassTransient,
				Details: map[string]any{
					"kind":       iErr.job.Kind,
					"job_id":     iErr.job.ID,
					"started_at": iErr.job.StartedAt.UTC().Format(time.RFC3339),
				},
			},
			Status: http.StatusConflict,
		})
		return
	}
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

// inflightErr is the sentinel error returned from the /sync writer tx
// when a concurrent sync is already running for the target repo. The
// embedded InflightJob carries kind/id/started_at for the 409 envelope
// details. Local to this file — do not export.
type inflightErr struct {
	job metadata.InflightJob
}

func (e *inflightErr) Error() string { return "mirror_sync_in_flight_tx" }

// writeMirrorSyncInFlight emits the generalized 409 envelope for a
// concurrent-sync collision. Uses a reader-pool GetInflight call to
// populate {kind, job_id, started_at}; if that lookup races with the
// job finishing (a vanishingly rare window since the caller just
// observed inflight > 0), falls back to a best-effort envelope with
// empty details. Response shape parity with the writer-tx emission
// further down handleSync.
func writeMirrorSyncInFlight(w http.ResponseWriter, r *http.Request, d SyncRESTDeps, repoID int64) {
	details := map[string]any{}
	// Use a short-lived writer tx for the lookup. The reader pool could
	// miss a very-recent insert under SQLite's WAL semantics; the writer
	// tx guarantees we see it. This is a single-row read on an indexed
	// column — cheap even on the writer.
	_ = d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		job, exists, err := d.SyncJobs.GetInflightTx(r.Context(), tx, repoID)
		if err != nil {
			return err
		}
		if exists {
			details["kind"] = job.Kind
			details["job_id"] = job.ID
			details["started_at"] = job.StartedAt.UTC().Format(time.RFC3339)
		}
		return nil
	})
	httperr.Write(w, r, &httperr.Error{
		Envelope: httperr.Envelope{
			Code:    codeMirrorSyncInFlight,
			Message: "A sync is already running for this repo.",
			Class:   httperr.ClassTransient,
			Details: details,
		},
		Status: http.StatusConflict,
	})
}
