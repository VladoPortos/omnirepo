package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// Deps bundles every subsystem the D-36 admin REST surface needs. Populated
// by internal/app.Run at startup.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	Sessions *metadata.SessionsRepo
	APIKeys  *metadata.APIKeysRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo
	Repos         *metadata.ReposRepo
	Settings      *metadata.SettingsRepo
	UpstreamCreds *metadata.UpstreamCredsRepo

	Holder   *omrtls.CertHolder
	DataRoot string
	Audit    audit.Logger
	Trash    storage.Trash
	Locks    storage.Locks

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
		Clock:          d.Clock,
		SessionTTL:     d.sessionTTL(),
		SessionHardTTL: d.sessionHardTTL(),
	}

	r.Route("/api/v1", func(r chi.Router) {
		// Unauthenticated: login.
		r.Post("/auth/login", d.handleLogin)

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

			r.Get("/me", d.handleMe)
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
		})
	})
}

// membershipResolver populates the per-request membership set used by
// auth.Can's project-scoped checks (TEN-17, REPO-04/06). Runs AFTER
// SessionOrAPIKey so the actor is already on ctx.
func (d Deps) membershipResolver() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := auth.ActorFromContext(r.Context())
			if ok && actor.Kind == auth.ActorKindUser && actor.ID != 0 {
				ids, err := d.Members.ListProjectIDsForUser(r.Context(), actor.ID)
				if err == nil {
					r = r.WithContext(auth.WithProjectMembership(r.Context(), ids))
				}
			} else if actor.Kind == auth.ActorKindAPIKey && actor.ProjectScope != nil {
				// Project-owned API key — membership is a singleton of that project.
				r = r.WithContext(auth.WithProjectMembership(r.Context(), []int64{*actor.ProjectScope}))
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.VerifyFixedCost("")
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
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
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		d.recordAudit(r, audit.Event{Kind: audit.EvtAuthLoginFailure, TargetKind: "user", TargetID: req.Login, Outcome: "user_not_found"})
		return
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, req.Password)
	if err != nil || !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		uid := u.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtAuthLoginFailure, ActorUserID: &uid, TargetKind: "user", TargetID: u.Login, Outcome: "wrong_password"})
		return
	}
	tok, err := auth.GenerateSession()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	issued := d.clock()
	expires := issued.Add(d.sessionTTL())
	if _, err := d.Sessions.Create(r.Context(), u.ID, tok.Prefix, tok.SHA256, issued, expires); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if req.New == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "new password empty")
		return
	}
	u, err := d.Users.FindByID(r.Context(), a.ID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	ok, _ := auth.VerifyPassword(u.PasswordHash, req.Current)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "wrong current password")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if err := d.Users.UpdatePasswordHash(r.Context(), a.ID, hash); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	uid := a.ID
	d.recordAudit(r, audit.Event{Kind: audit.EvtAuthPasswordChanged, ActorUserID: &uid, TargetKind: "user", TargetID: u.Login})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	a, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	u, err := d.Users.FindByID(r.Context(), a.ID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	writeJSON(w, http.StatusOK, MeResponse{
		ID: u.ID, Login: u.Login, Email: u.Email,
		IsSuperAdmin: u.IsSuperAdmin, MustChangePassword: u.MustChangePassword,
	})
}

func (d Deps) handleDeleteMe(w http.ResponseWriter, r *http.Request) {
	a, _ := auth.ActorFromContext(r.Context())
	if err := d.Users.Delete(r.Context(), a.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	uid := a.ID
	d.recordAudit(r, audit.Event{Kind: audit.EvtUserDeleted, ActorUserID: &uid, TargetKind: "user", TargetID: a.Login})
	auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if err := auth.LoginValid(req.Login); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		return
	}
	if req.Email == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "email empty")
		return
	}
	otp := auth.OneTimePassword()
	hash, err := auth.HashPassword(otp)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	_, err = d.Users.Create(r.Context(), req.Login, req.Email, hash, false, true)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
			writeJSONError(w, http.StatusConflict, ErrConflict, "login exists")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "user not found")
		return
	}
	if err := d.Users.Delete(r.Context(), u.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtUserDeleted, ActorUserID: &uid, TargetKind: "user", TargetID: u.Login})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if err := auth.ProjectNameValid(req.Name); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		return
	}
	id, err := d.Projects.Create(r.Context(), req.Name, req.DescriptionMD)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
			writeJSONError(w, http.StatusConflict, ErrConflict, "project exists")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtProjectCreated, ActorUserID: &uid, TargetKind: "project", TargetID: req.Name})
		// Creator becomes first member.
		_ = d.Members.Add(r.Context(), id, a.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": req.Name})
}

func (d Deps) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}
	if err := d.Projects.SoftDelete(r.Context(), p.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	u, err := d.Users.FindByLogin(r.Context(), login)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "user")
		return
	}
	if err := d.Members.Add(r.Context(), p.ID, u.ID); err != nil {
		// PK-conflict → 409.
		writeJSONError(w, http.StatusConflict, ErrConflict, "already a member")
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
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	u, err := d.Users.FindByLogin(r.Context(), login)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "user")
		return
	}
	if err := d.Members.Remove(r.Context(), p.ID, u.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtMemberRemoved, ActorUserID: &uid, TargetKind: "project", TargetID: p.Name, Details: map[string]any{"user": u.Login}})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	var req CreateRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if err := auth.ProjectNameValid(req.Name); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		return
	}
	if _, ok := validRepoTypes[req.Type]; !ok {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "invalid type")
		return
	}
	id, err := d.Repos.Create(r.Context(), p.ID, req.Type, req.Name, req.DescriptionMD, req.AutoScan, req.BlockOnSeverity, req.PublicRead)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
			writeJSONError(w, http.StatusConflict, ErrConflict, "repo exists")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{Kind: audit.EvtRepoCreated, ActorUserID: &uid, TargetKind: "repo", TargetID: p.Name + "/" + req.Type + "/" + req.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": req.Name, "type": req.Type})
}

func (d Deps) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "name")
	typ := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project")
		return
	}
	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, typ, repoName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo")
		return
	}
	if err := d.Repos.SoftDelete(r.Context(), rr.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
		onDisk := filepath.Join(d.DataRoot, "repos", projectName, typ, repoName)
		if _, err := d.Trash.Move(r.Context(), onDisk, "repo", rr.ID); err != nil {
			// Tree may not exist for a freshly-created empty repo; that's not a failure.
			// Use errors.Is(os.ErrNotExist) instead of string-matching — rename
			// error wrapping via fmt.Errorf(..., %w) preserves the sentinel.
			if !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrNotExist) {
				// Log via audit, do not fail the request — DB row is already soft-deleted.
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
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "multipart parse: "+err.Error())
		return
	}
	certBytes, err := readFormFile(r, "cert")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "cert: "+err.Error())
		return
	}
	keyBytes, err := readFormFile(r, "key")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "key: "+err.Error())
		return
	}
	if err := omrtls.ApplyUpload(r.Context(), certBytes, keyBytes, d.DataRoot, d.Holder); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
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
