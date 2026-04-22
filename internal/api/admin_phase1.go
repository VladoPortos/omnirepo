package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	s3backend "github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// Deps bundles every subsystem the D-36 admin REST surface needs. Populated
// by internal/app.Run at startup.
type Deps struct {
	DB            *metadata.DB
	Users         *metadata.UsersRepo
	Sessions      *metadata.SessionsRepo
	APIKeys       *metadata.APIKeysRepo
	Projects      *metadata.ProjectsRepo
	Members       *metadata.MembersRepo
	Repos         *metadata.ReposRepo
	Settings      *metadata.SettingsRepo
	UpstreamCreds *metadata.UpstreamCredsRepo

	// Phase 04-05: S3 access-key CRUD. nil-safe — when nil, the routes
	// are not mounted.
	S3Keys *metadata.S3KeysRepo
	S3AEAD *omrcrypto.AEAD

	// S3Backend is the gofakes3-backed bucket/object store. nil-safe —
	// when nil, the REST bucket-provisioning routes are not mounted.
	// Walkthrough 2026-04-17: operators need a non-test path to create
	// buckets; see internal/api/s3_buckets.go.
	S3Backend *s3backend.Backend

	// S3ObjectsRepo is used by the bucket-objects browsing endpoint
	// (`GET /api/v1/projects/{name}/s3-buckets/{bucket}/objects`). It
	// reads rows from the s3_objects table backing the same bucket that
	// gofakes3 writes through S3Backend. Required when S3Backend is set.
	S3ObjectsRepo *metadata.S3ObjectsRepo

	Holder   *omrtls.CertHolder
	DataRoot string
	Audit    audit.Logger
	Trash    storage.Trash
	Locks    storage.Locks

	// TrivyDBDir is the directory admin_trivy reads/writes. When empty,
	// falls back to DataRoot/trivy/db. Set by app.Run from cfg.Trivy.DBPath
	// so operator overrides line up with the runner (internal/scan).
	// Audit finding #4.
	TrivyDBDir string

	// TLSCertPath / TLSKeyPath are where the admin TLS upload endpoint
	// persists live certs. When empty, fall back to DataRoot/certs/server.*.
	// Set by app.Run from cfg.TLS.* so operator overrides work. Audit
	// finding #4.
	TLSCertPath string
	TLSKeyPath  string

	// ScanDeps is the Plan 02-09 scan REST surface dependency bundle.
	// nil-safe — when nil, scan endpoints are not mounted.
	ScanDeps *ScansDeps

	// OCIActions is the Plan 02-10 pull-external + promote dependency bundle.
	// nil-safe — when nil, the two endpoints are not mounted.
	OCIActions *OCIActionsDeps

	// GCDeps is the Plan 02-12 admin GC trigger dependency bundle.
	// nil-safe — when nil, /api/v1/admin/gc is not mounted.
	GCDeps *GCDeps

	// SyncDeps is the Plan 03-06 SYNC-05 sync REST endpoint dependency
	// bundle. nil-safe — when nil, /api/v1/projects/{name}/repos/{type}/{repo}/sync
	// is not mounted. Pre-populated by app.Run with the
	// ActorResolver shim that bridges auth.Actor to httpx.SyncActor.
	SyncDeps *SyncRESTAdapter

	// RepoCreateHook is invoked INSIDE the repo-create writer tx so the
	// hook's writes (e.g. RPM/DEB signing-key generation, Phase 03 Plan 04
	// D-02) commit atomically with the repos INSERT. Returns optional
	// per-type extras to fold into the API response (e.g. fingerprint).
	// nil = no-op (used by tests that don't care about per-type extras).
	RepoCreateHook RepoCreateHookFn

	// Clock is used for session/token issuance. Defaults to time.Now().UTC.
	Clock func() time.Time

	// SessionTTL controls the session sliding-window lifetime (D-07).
	// Zero → 12h default.
	SessionTTL time.Duration

	// SessionHardTTL is the absolute cap from issuance beyond which a
	// session is rejected regardless of recent activity (D-07: 7d).
	// Zero → 7d default.
	SessionHardTTL time.Duration
}

func (d Deps) clock() time.Time {
	if d.Clock == nil {
		return time.Now().UTC()
	}
	return d.Clock().UTC()
}

func (d Deps) sessionTTL() time.Duration {
	if d.SessionTTL == 0 {
		return 12 * time.Hour
	}
	return d.SessionTTL
}

func (d Deps) sessionHardTTL() time.Duration {
	if d.SessionHardTTL == 0 {
		return 7 * 24 * time.Hour
	}
	return d.SessionHardTTL
}

// Mount installs every D-36 endpoint onto r under the /api/v1 prefix. Handlers
// that mutate state are wrapped in RequireCan (or RequireCanWith, for project-
// scoped actions) AFTER SessionOrAPIKey has populated the actor on ctx.
//
// Healthz and Readyz are NOT mounted here — they live at the root (/healthz,
// /readyz) and the app.Run orchestrator wires them directly on the bare
// router so they bypass the api middleware chain.
func Mount(r chi.Router, d Deps) {
	midDeps := authmw.Deps{
		Users:          d.Users,
		Sessions:       d.Sessions,
		APIKeys:        d.APIKeys,
		// F-T8: wire Projects so SessionOrAPIKey accepts the
		// project:<name>:<key> Basic variant as well as the generic
		// user:<api-key> shape. Without this, /api/v1 silently falls back
		// to 401 for project-scoped keys sent via Basic — mismatched
		// behaviour vs. the protocol endpoints (BasicOrAPIKey already
		// honored both shapes).
		Projects:       d.Projects,
		Clock:          d.Clock,
		SessionTTL:     d.sessionTTL(),
		SessionHardTTL: d.sessionHardTTL(),
	}

	// API-02: Swagger UI at /api/docs, OpenAPI spec at /api/v1/openapi.yaml.
	r.Get("/api/v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Write(openapiSpec)
	})
	r.Get("/api/docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/index.html", http.StatusMovedPermanently)
	})
	r.Get("/api/docs/*", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/index.html", http.StatusMovedPermanently)
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Unauthenticated: first-run setup probes + super-admin create.
		// These must sit outside SessionOrAPIKey because there is no user
		// to authenticate as until the setup endpoint has been called.
		r.Get("/setup/status", d.handleSetupStatus)
		r.Post("/setup/superadmin", d.handleSetupSuperAdmin)

		// Unauthenticated: login + auth check.
		r.Post("/auth/login", d.handleLogin)
		r.With(authmw.OptionalSessionOrAPIKey(midDeps)).Get("/me", d.handleMe)

		// Authenticated routes use SessionOrAPIKey; projects/repos/members
		// additionally pre-resolve project membership before RequireCanWith.
		r.Group(func(r chi.Router) {
			r.Use(authmw.SessionOrAPIKey(midDeps))
			r.Use(d.membershipResolver())

			r.Post("/auth/logout", d.handleLogout)
			r.With(authmw.RequireCanWith(auth.ActionChangeOwnPassword, func(r *http.Request) auth.Target {
				if a, ok := auth.ActorFromContext(r.Context()); ok {
					return auth.Target{Kind: "user", UserID: a.ID}
				}
				return auth.Target{}
			})).Post("/auth/change-password", d.handleChangePassword)
			r.With(authmw.RequireCanWith(auth.ActionDeleteOwnUser, func(r *http.Request) auth.Target {
				if a, ok := auth.ActorFromContext(r.Context()); ok {
					return auth.Target{Kind: "user", UserID: a.ID}
				}
				return auth.Target{}
			})).Delete("/me", d.handleDeleteMe)

			// Admin users.
			r.With(authmw.RequireCan(auth.ActionCreateUser)).
				Post("/admin/users", d.handleCreateUser)
			r.With(authmw.RequireCan(auth.ActionDeleteUser)).
				Delete("/admin/users/{login}", d.handleDeleteUser)

			// TLS upload (super-admin only).
			r.With(authmw.RequireCan(auth.ActionUploadTLSCert)).
				Post("/admin/tls/upload", d.handleTLSUpload)

			// Projects.
			r.With(authmw.RequireCan(auth.ActionCreateProject)).
				Post("/projects", d.handleCreateProject)
			r.With(authmw.RequireCanWith(auth.ActionDeleteProject, d.resolveProjectTargetFromURL)).
				Delete("/projects/{name}", d.handleDeleteProject)

			// Project members (project-scoped).
			r.With(authmw.RequireCanWith(auth.ActionAddProjectMember, d.resolveProjectTargetFromURL)).
				Post("/projects/{name}/members/{login}", d.handleAddMember)
			r.With(authmw.RequireCanWith(auth.ActionRemoveProjectMember, d.resolveProjectTargetFromURL)).
				Delete("/projects/{name}/members/{login}", d.handleRemoveMember)

			// Repos (project-scoped).
			r.With(authmw.RequireCanWith(auth.ActionCreateRepo, d.resolveProjectTargetFromURL)).
				Post("/projects/{name}/repos", d.handleCreateRepo)
			r.With(authmw.RequireCanWith(auth.ActionDeleteRepo, d.resolveProjectTargetFromURL)).
				Delete("/projects/{name}/repos/{type}/{repo}", d.handleDeleteRepo)
			// Repo detail — project members (or super-admin) can view metadata
			// for the detail page. Uses the project-scoped target resolver so
			// ActionRepoRead's "member or super-admin" branch applies.
			r.With(authmw.RequireCanWith(auth.ActionRepoRead, d.resolveProjectTargetFromURL)).
				Get("/projects/{name}/repos/{type}/{repo}", d.handleGetRepo)
			// Phase 02-11: PATCH settings + POST /wipe (REPO-05, REPO-07, D-34, D-35).
			r.With(authmw.RequireCanWith(auth.ActionUpdateRepo, d.resolveProjectTargetFromURL)).
				Patch("/projects/{name}/repos/{type}/{repo}", d.handlePatchRepo)
			r.With(authmw.RequireCanWith(auth.ActionWipeRepo, d.resolveProjectTargetFromURL)).
				Post("/projects/{name}/repos/{type}/{repo}/wipe", d.handleWipeRepo)

			// Phase 02-02: upstream creds CRUD. Handlers re-check project
			// membership inline (ActionManageUpstreamCreds) so route-level
			// RequireCanWith is not needed here — this lets us return 401
			// distinct from 403 and 404.
			d.mountUpstreamCreds(r)

			// Phase 04-05: S3 access-key CRUD (create/list/revoke).
			// Handlers re-check project membership inline
			// (ActionManageS3Keys) so route-level RequireCanWith is not
			// needed — same pattern as upstream_creds.
			d.mountS3Keys(r)

			// Walkthrough 2026-04-17: S3 bucket provisioning (create/list).
			// gofakes3's CreateBucket path is disabled in production; this
			// is the operator-facing route that was previously missing.
			d.mountS3Buckets(r)

			// Phase 02-09: scan REST endpoints (manual rescan, scan list,
			// scan detail, vulnerabilities, SBOM download). Mount only when
			// ScanDeps is wired by app.Run.
			d.mountScans(r)

			// Phase 02-10: OCI pull-external + promote (D-04, D-05, D-12).
			// Mounted inside the already-auth'd subtree so SessionOrAPIKey
			// has populated the actor on ctx; the oci.PullExternalREST /
			// PromoteREST handlers re-check membership inline (same pattern
			// as upstream-creds) so they can return distinct 401/403/404.
			RegisterOCIActionsRoutes(r, d.OCIActions)

			// Phase 02-12: super-admin garbage collection trigger
			// (D-37, OPS-06). RequireCan(ActionTriggerGC) gate inside.
			d.mountAdminGC(r)

			// Phase 07 / D-06: super-admin background-jobs read-only
			// summary for the Dashboard C-4 card. Reuses ActionTriggerGC
			// as the policy gate (no new action introduced).
			d.mountAdminJobs(r)

			// Phase 05-03: admin audit, maintenance, trash, settings,
			// Trivy DB, TLS history, full user CRUD.
			d.mountAdminAudit(r)
			d.mountAdminMaintenance(r)
			d.mountAdminTrash(r)
			d.mountAdminSettings(r)
			d.mountAdminTrivy(r)
			d.mountAdminDBHealth(r) // Phase 10 DBHEALTH-01..07
			d.mountAdminTLSHistory(r)
			d.mountAdminUsersFull(r)

			// Phase 03 Plan 06: SYNC-05 sync REST endpoint. Mounted inside
			// the SessionOrAPIKey subtree so the ActorResolver finds the
			// actor on ctx; the handler re-checks project membership inline.
			RegisterSyncRoutes(r, d.SyncDeps)

			// Phase 05-04: search, profile, API keys, projects list,
			// repos list, dashboard (any auth, filtered by membership).
			d.mountSearch(r)
			d.mountProfile(r)
			d.mountAPIKeys(r)
			d.mountMeS3Keys(r)
			d.mountProjectAPIKeys(r)
			d.mountProjectsFull(r)
			d.mountReposList(r)
			d.mountRepoContent(r)
			d.mountDashboard(r)
			d.mountGitBrowse(r)
		})
	})
}

// membershipResolver populates the per-request membership set used by
// auth.Can's project-scoped checks (TEN-17, REPO-04/06). Runs AFTER
// SessionOrAPIKey so the actor is already on ctx.
func (d Deps) membershipResolver() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if actor, ok := auth.ActorFromContext(r.Context()); ok {
				r = r.WithContext(auth.ResolveMembership(r.Context(), actor, d.Members))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// resolveProjectTargetFromURL builds an auth.Target from the {name} chi url
// parameter so RequireCanWith can project-scope the allow/deny decision.
func (d Deps) resolveProjectTargetFromURL(r *http.Request) auth.Target {
	name := chi.URLParam(r, "name")
	if name == "" {
		return auth.Target{}
	}
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		return auth.Target{}
	}
	return auth.Target{Kind: "project", ProjectID: p.ID}
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

func (d Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	// WR-04: close the user-enumeration timing oracle by ensuring every
	// negative path burns the same argon2id CPU+memory cost as a real
	// password verification. Any early return on a failure path MUST have
	// called auth.VerifyFixedCost first.
	var req LoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		auth.VerifyFixedCost("")
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "Invalid login or password.")
		return
	}
	// Drop the LoginValid short-circuit: a malformed login can never match
	// the UNIQUE index, so FindByLogin will return ErrNotFound just like a
	// well-formed but unknown login. Treating both the same hides the
	// "login format valid" signal from response timing.
	u, err := d.Users.FindByLogin(r.Context(), req.Login)
	if err != nil {
		// Unknown user — burn argon2 anyway to match the wrong-password
		// latency. Constant-time argument since req.Password is the same
		// bytes an attacker would have sent for a real user.
		auth.VerifyFixedCost(req.Password)
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "Invalid login or password.")
		d.recordAudit(r, audit.Event{Kind: audit.EvtAuthLoginFailure, TargetKind: "user", TargetID: req.Login, Outcome: "user_not_found"})
		return
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, req.Password)
	if err != nil || !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "Invalid login or password.")
		uid := u.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtAuthLoginFailure, ActorUserID: &uid, TargetKind: "user", TargetID: u.Login, Outcome: "wrong_password"})
		return
	}
	tok, err := auth.GenerateSession()
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	issued := d.clock()
	expires := issued.Add(d.sessionTTL())
	if _, err := d.Sessions.Create(r.Context(), u.ID, tok.Prefix, tok.SHA256, issued, expires); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	auth.SetSessionCookie(w, tok.Plaintext, r.TLS != nil)
	uid := u.ID
	d.recordAudit(r, audit.Event{Kind: audit.EvtAuthLoginSuccess, ActorUserID: &uid, TargetKind: "user", TargetID: u.Login})
	writeJSON(w, http.StatusOK, LoginResponse{
		Login:              u.Login,
		IsSuperAdmin:       u.IsSuperAdmin,
		MustChangePassword: u.MustChangePassword,
	})
}

func (d Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
		if prefix, ok := auth.SessionPrefix(c.Value); ok {
			sha := auth.SessionSHA256(c.Value)
			if s, err := d.Sessions.FindByPrefixSha(r.Context(), prefix, sha); err == nil {
				_ = d.Sessions.Delete(r.Context(), s.ID)
			}
		}
	}
	auth.ClearSessionCookie(w)
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtAuthLogout, ActorUserID: &uid})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	a, _ := auth.ActorFromContext(r.Context())
	var req ChangePasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if req.New == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "new password empty")
		return
	}
	u, err := d.Users.FindByID(r.Context(), a.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "Your session is no longer valid.")
		return
	}
	ok, _ := auth.VerifyPassword(u.PasswordHash, req.Current)
	if !ok {
		// Audit the failure so an admin can correlate with login-failure
		// spikes. Attempting a self-service change with the wrong current
		// password is the same threat surface as login brute-force, and
		// should be equally observable (F-02.2).
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtAuthPasswordChanged,
			ActorUserID: &uid,
			TargetKind:  "user",
			TargetID:    u.Login,
			Outcome:     "wrong_password",
		})
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "wrong current password")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if err := d.Users.UpdatePasswordHash(r.Context(), a.ID, hash); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	// HI-01: invalidate every other session for this user. Preserves the
	// current browser session (the one that just authenticated to change
	// the password) so the user isn't logged out of their own tab.
	currentSessionID := sessionIDFromCookie(r, d.Sessions)
	if currentSessionID != 0 {
		_ = d.Sessions.DeleteAllForUserExcept(r.Context(), a.ID, currentSessionID)
	} else {
		_ = d.Sessions.DeleteAllForUser(r.Context(), a.ID)
	}
	uid := a.ID
	d.recordAudit(r, audit.Event{
		Kind:        audit.EvtAuthPasswordChanged,
		ActorUserID: &uid,
		TargetKind:  "user",
		TargetID:    u.Login,
		Outcome:     "ok",
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sessionIDFromCookie resolves the current request's session cookie to a row
// id. Returns 0 if no cookie, invalid cookie, or session not found. Used by
// password-change flows to preserve the caller's session while invalidating
// all other sessions for the same user.
func sessionIDFromCookie(r *http.Request, sessions *metadata.SessionsRepo) int64 {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil || c.Value == "" {
		return 0
	}
	prefix, ok := auth.SessionPrefix(c.Value)
	if !ok {
		return 0
	}
	s, err := sessions.FindByPrefixSha(r.Context(), prefix, auth.SessionSHA256(c.Value))
	if err != nil {
		return 0
	}
	return s.ID
}

func (d Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	a, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	u, err := d.Users.FindByID(r.Context(), a.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	resp := MeResponse{
		Id: u.ID, Login: u.Login, Email: u.Email,
		IsSuperAdmin: u.IsSuperAdmin, MustChangePassword: u.MustChangePassword,
	}
	if u.AvatarSeed != "" {
		s := u.AvatarSeed
		resp.AvatarSeed = &s
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d Deps) handleDeleteMe(w http.ResponseWriter, r *http.Request) {
	a, _ := auth.ActorFromContext(r.Context())
	if err := d.Users.Delete(r.Context(), a.ID); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	// F-03.6 (wt3): Users.Delete is a soft-delete, so the FK cascade on
	// sessions and api_keys never fires. The middleware rejects
	// soft-deleted users on subsequent auth (FindByID scopes to
	// deleted_at IS NULL) so the orphaned rows are inert — but leaving
	// them around keeps the partial unique indexes on (owner_user_id,
	// name) claiming slots for logins that no longer exist, and means a
	// future regression that loosens the middleware check would silently
	// resurrect every session / key the departed user ever held. Belt
	// and braces: drop sessions, revoke live api_keys. Best-effort — any
	// error is logged but doesn't fail the delete (the account is
	// already soft-deleted + the cookie will be cleared below).
	if err := d.Sessions.DeleteAllForUser(r.Context(), a.ID); err != nil {
		slog.Warn("delete_me: sessions cleanup", "user_id", a.ID, "err", err)
	}
	if err := d.APIKeys.RevokeAllByUser(r.Context(), a.ID); err != nil {
		slog.Warn("delete_me: api_keys revoke", "user_id", a.ID, "err", err)
	}
	uid := a.ID
	d.recordAudit(r, audit.Event{Kind: audit.EvtUserDeleted, ActorUserID: &uid, TargetKind: "user", TargetID: a.Login})
	auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if !decodeJSONBody(w, r, maxAdminJSONBodyBytes, &req) {
		return
	}
	if err := auth.LoginValid(req.Login); err != nil {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		return
	}
	if req.Email == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "email empty")
		return
	}
	otp := auth.OneTimePassword()
	hash, err := auth.HashPassword(otp)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	_, err = d.Users.Create(r.Context(), req.Login, req.Email, hash, false, true)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "login exists")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtUserCreated, ActorUserID: &uid, TargetKind: "user", TargetID: req.Login})
	}
	writeJSON(w, http.StatusOK, CreateUserResponse{Login: req.Login, OneTimePassword: otp})
}

func (d Deps) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	login := chi.URLParam(r, "login")
	u, err := d.Users.FindByLogin(r.Context(), login)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "user not found")
		return
	}

	// Safety checks (F-02.3):
	//   1. An admin cannot delete their own account via this endpoint.
	//      Self-deletion is its own workflow at DELETE /me, which adds
	//      explicit confirmation and signs the actor out cleanly.
	//   2. The last live super-admin cannot be deleted. Removing the
	//      only admin leaves the instance permanently unable to manage
	//      itself (no way back into /admin/*), which is a
	//      practically-irrecoverable state for an air-gapped deployment.
	//      The last-admin check runs inside the same WriteTx as the
	//      soft-delete (via Users.DeleteEnforceLastSuperAdmin), so two
	//      concurrent delete requests cannot race past the check.
	actor, _ := auth.ActorFromContext(r.Context())
	if actor.ID == u.ID {
		writeJSONError(w, r, http.StatusConflict, ErrConflict, "cannot delete yourself — use the self-service delete in your profile")
		return
	}

	if err := d.Users.DeleteEnforceLastSuperAdmin(r.Context(), u.ID); err != nil {
		if errors.Is(err, metadata.ErrLastSuperAdmin) {
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "cannot delete the last super-admin — promote another user first")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	uid := actor.ID
	d.recordAudit(r, audit.Event{Kind: audit.EvtUserDeleted, ActorUserID: &uid, TargetKind: "user", TargetID: u.Login})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if !decodeJSONBody(w, r, maxAdminJSONBodyBytes, &req) {
		return
	}
	if err := auth.ProjectNameValid(req.Name); err != nil {
		writeFieldValidationError(w, r, ErrValidationFailed, "name", err.Error())
		return
	}

	// Audit finding #7: project insert and creator-membership insert must
	// commit or roll back together — otherwise a failed Members.Add left
	// an orphan project that nobody could delete.
	var id int64
	actor, hasActor := auth.ActorFromContext(r.Context())
	txErr := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		var insErr error
		id, insErr = d.Projects.CreateInTx(r.Context(), tx, req.Name, req.DescriptionMD)
		if insErr != nil {
			return insErr
		}
		if hasActor && actor.ID != 0 {
			if err := d.Members.AddInTx(r.Context(), tx, id, actor.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		if strings.Contains(txErr.Error(), "UNIQUE") || strings.Contains(txErr.Error(), "constraint") {
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "project exists")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if hasActor {
		uid := actor.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtProjectCreated, ActorUserID: &uid, TargetKind: "project", TargetID: req.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": req.Name})
}

func (d Deps) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}
	if err := d.Projects.SoftDelete(r.Context(), p.ID); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtProjectDeleted, ActorUserID: &uid, TargetKind: "project", TargetID: p.Name})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleAddMember(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	login := chi.URLParam(r, "login")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	u, err := d.Users.FindByLogin(r.Context(), login)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "user")
		return
	}
	if err := d.Members.Add(r.Context(), p.ID, u.ID); err != nil {
		// PK-conflict → 409.
		writeJSONError(w, r, http.StatusConflict, ErrConflict, "already a member")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtMemberAdded, ActorUserID: &uid, TargetKind: "project", TargetID: p.Name, Details: map[string]any{"user": u.Login}})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	login := chi.URLParam(r, "login")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	u, err := d.Users.FindByLogin(r.Context(), login)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "user")
		return
	}
	if err := d.Members.Remove(r.Context(), p.ID, u.ID); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtMemberRemoved, ActorUserID: &uid, TargetKind: "project", TargetID: p.Name, Details: map[string]any{"user": u.Login}})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// RepoCreateHookFn is invoked inside the repo-create writer tx so any
// secondary INSERTs (signing keys, etc.) commit atomically with the repos
// INSERT. Returns optional extras to fold into the API response. Errors
// abort the tx and surface as 500 to the caller.
type RepoCreateHookFn func(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName string) (extras map[string]any, err error)

func (d Deps) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	var req CreateRepoRequest
	if !decodeJSONBody(w, r, maxAdminJSONBodyBytes, &req) {
		return
	}
	if err := auth.RepoNameValid(req.Name); err != nil {
		writeFieldValidationError(w, r, ErrValidationFailed, "name", err.Error())
		return
	}
	if _, ok := validRepoTypes[req.Type]; !ok {
		writeFieldValidationError(w, r, ErrValidationFailed, "type", "invalid type")
		return
	}

	// Phase 8 Plan 01 (MIRROR-01..07): optional mirror-flag validation.
	// Envelope codes: codeRepoMirror* constants defined in repos.go.
	if req.IsMirror {
		if _, ok := mirrorSupportedTypes[req.Type]; !ok {
			writeJSONError(w, r, http.StatusBadRequest, codeRepoMirrorTypeUnsupported,
				"mirror flag only supported for deb/rpm/pypi/helm")
			return
		}
		if !validateMirrorUpstreamURL(req.MirrorUpstreamURL, req.Type) {
			writeJSONError(w, r, http.StatusBadRequest, codeRepoMirrorURLInvalid,
				"upstream URL must be http(s) with a host")
			return
		}
		ok, canonical := validateMirrorFilter(req.Type, req.MirrorFilter)
		if !ok {
			writeJSONError(w, r, http.StatusBadRequest, codeRepoMirrorFilterInvalid,
				"mirror_filter JSON does not match the protocol SyncFilter shape")
			return
		}
		req.MirrorFilter = canonical
		if req.MirrorCredID != nil {
			ok, _ := mirrorCredOwnership(r.Context(), d.UpstreamCreds, p.ID, *req.MirrorCredID)
			if !ok {
				writeJSONError(w, r, http.StatusBadRequest, codeRepoMirrorCredWrongProject,
					"mirror_cred_id must belong to the same project as the repo")
				return
			}
		}
		// Phase 11 Plan 03 Task 3 (OCIHELM-05 / D-04): Docker Hub cred gate.
		// Only applies to helm mirrors where the upstream scheme is oci://
		// targeting registry-1.docker.io — refuseDockerHubWithoutCred
		// short-circuits to nil for every other combination. The gate fires
		// on the (URL, credKind) tuple, so we never skip it just because
		// UpstreamCreds wiring is absent — a missing cred_id is exactly the
		// case we MUST refuse. UpstreamCreds is only consulted to resolve a
		// cred's kind when cred_id IS supplied; the validator treats any
		// non-empty kind as "something attached" per plan 11-02 D-06.
		if req.Type == "helm" {
			credKind := ""
			if req.MirrorCredID != nil && *req.MirrorCredID > 0 && d.UpstreamCreds != nil {
				if cm, cerr := d.UpstreamCreds.Get(r.Context(), p.ID, *req.MirrorCredID); cerr == nil && cm != nil {
					credKind = string(cm.Kind)
				}
			}
			if env := refuseDockerHubWithoutCred(req.MirrorUpstreamURL, credKind); env != nil {
				writeEnvelope(w, r, env)
				return
			}
		}
	}

	// Compose repo INSERT + optional hook + optional mirror-config UPDATE
	// in a single writer tx so a hook failure (e.g. RPM signing-key
	// generation breaking) rolls back the repos row (T-03-04-06).
	var (
		id     int64
		extras map[string]any
	)
	txErr := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		var insErr error
		id, insErr = d.Repos.CreateInTx(r.Context(), tx, p.ID, req.Type, req.Name, req.DescriptionMD, req.AutoScan, req.BlockOnSeverity, req.PublicRead)
		if insErr != nil {
			return insErr
		}
		if req.IsMirror {
			mc := metadata.MirrorConfig{
				IsMirror:    true,
				UpstreamURL: req.MirrorUpstreamURL,
				FilterJSON:  string(req.MirrorFilter),
				CredID:      req.MirrorCredID,
				ScanOnSync:  req.ScanOnSync,
			}
			if err := d.Repos.SetMirrorConfigInTx(r.Context(), tx, id, mc); err != nil {
				return err
			}
		}
		if d.RepoCreateHook != nil {
			ex, herr := d.RepoCreateHook(r.Context(), tx, id, req.Type, p.Name, req.Name)
			if herr != nil {
				return herr
			}
			extras = ex
		}
		return nil
	})
	if txErr != nil {
		if strings.Contains(txErr.Error(), "UNIQUE") || strings.Contains(txErr.Error(), "constraint") {
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "repo exists")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtRepoCreated, ActorUserID: &uid, TargetKind: "repo", TargetID: p.Name + "/" + req.Type + "/" + req.Name})
		if fp, ok := extras["fingerprint"].(string); ok && fp != "" {
			d.recordAudit(r, audit.Event{Kind: audit.EvtSigningKeyCreated, ActorUserID: &uid, TargetKind: "repo", TargetID: p.Name + "/" + req.Type + "/" + req.Name, Details: map[string]any{"fingerprint": fp, "key_kind": "gpg_rsa4096"}})
		}
	}
	resp := map[string]any{"id": id, "name": req.Name, "type": req.Type}
	for k, v := range extras {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d Deps) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "name")
	typ := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, typ, repoName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo")
		return
	}
	if err := d.Repos.SoftDelete(r.Context(), rr.ID); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	// REPO-06: move on-disk tree to trash/<ts>-repo-<id>/ if it exists.
	//
	// WR-06 defense-in-depth: re-validate every URL-sourced segment that
	// goes into filepath.Join is a legal slug AND that the type is in the
	// known-good set. chi already blocks raw `/` and the DB lookup
	// rejects unknown triples, but the on-disk path is security-sensitive
	// (Trash.Move renames the tree into /var/lib/omnirepo/trash) so we
	// re-check here rather than trusting upstream invariants.
	onDiskSafe := true
	if err := auth.ProjectNameValid(projectName); err != nil {
		onDiskSafe = false
	}
	if _, ok := validRepoTypes[typ]; !ok {
		onDiskSafe = false
	}
	if err := auth.ProjectNameValid(repoName); err != nil {
		onDiskSafe = false
	}
	if onDiskSafe && d.Trash != nil {
		// Audit finding #3: git repos live at `.../<repo>.git`, not
		// `.../<repo>` — without the suffix the bare repo dir was leaked on
		// disk and the trash/restore flow was unreliable for type=git.
		onDisk := filepath.Join(d.DataRoot, "repos", projectName, typ, repoName)
		trashKind := "repo"
		if typ == "git" {
			onDisk += ".git"
			trashKind = "git-repo"
		}
		if _, err := d.Trash.Move(r.Context(), onDisk, trashKind, rr.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			// Tree may not exist for a freshly-created empty repo or an
			// unsynced git mirror. F-11 follow-up: Trash.Move now creates
			// a metadata-only sidecar in that case and returns the
			// os.ErrNotExist sentinel so the UI still has a restore
			// target (the DB row). Treat the sentinel as success; other
			// errors log via audit without failing the request — the
			// DB row is already soft-deleted.
			if !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrNotExist) {
				if a, ok := auth.ActorFromContext(r.Context()); ok {
					uid := a.ID
					d.recordAudit(r, audit.Event{Kind: audit.EvtRepoDeleted, ActorUserID: &uid, TargetKind: "repo", TargetID: projectName + "/" + typ + "/" + repoName, Outcome: "trash_move_failed", Details: map[string]any{"err": err.Error()}})
				}
			}
		}
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtRepoDeleted, ActorUserID: &uid, TargetKind: "repo", TargetID: projectName + "/" + typ + "/" + repoName})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleTLSUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "multipart parse: "+err.Error())
		return
	}
	certBytes, err := readFormFile(r, "cert")
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "cert: "+err.Error())
		return
	}
	keyBytes, err := readFormFile(r, "key")
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "key: "+err.Error())
		return
	}
	// Audit finding #4: honor cfg.TLS.{cert_path,key_path} (threaded via
	// Deps.TLSCertPath/TLSKeyPath) so admin upload writes where app boot
	// reads. Fall back to the legacy DataRoot/certs layout when unset.
	certFinal := d.TLSCertPath
	if certFinal == "" {
		certFinal = filepath.Join(d.DataRoot, "certs", "server.crt")
	}
	keyFinal := d.TLSKeyPath
	if keyFinal == "" {
		keyFinal = filepath.Join(d.DataRoot, "certs", "server.key")
	}
	layout := omrtls.UploadLayout{
		CertPath:   certFinal,
		KeyPath:    keyFinal,
		HistoryDir: filepath.Join(d.DataRoot, "certs", "uploaded"),
	}
	if err := omrtls.ApplyUploadAt(r.Context(), certBytes, keyBytes, layout, d.Holder); err != nil {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtTLSCertUploaded, ActorUserID: &uid, TargetKind: "tls"})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

var validRepoTypes = map[string]struct{}{
	"rpm": {}, "deb": {}, "pypi": {}, "docker": {}, "helm": {}, "git": {}, "raw": {},
}

// readFormFile loads the full contents of an uploaded multipart file field.
func readFormFile(r *http.Request, field string) ([]byte, error) {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// recordAudit best-effort records e. Audit failures are NOT surfaced to the
// client — the state change has already happened and audit durability is a
// secondary concern (OQ-9).
func (d Deps) recordAudit(r *http.Request, e audit.Event) {
	if d.Audit == nil {
		return
	}
	if e.IP == "" {
		e.IP = r.RemoteAddr
	}
	if e.UserAgent == "" {
		e.UserAgent = r.Header.Get("User-Agent")
	}
	_ = d.Audit.Record(r.Context(), e)
}
