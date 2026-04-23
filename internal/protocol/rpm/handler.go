// Package rpm — HTTP handler. Mounts the RPM-protocol surface at
// /<project>/rpm/<repo>/... per RPM-01..05.
//
// Routes:
//   GET    /public-key.asc           — armored public key (lock-free cache)
//   GET    /repodata/*               — repodata files (lock-free disk serve)
//   GET    /packages/{filename}      — .rpm download (severity-gated)
//   HEAD   /packages/{filename}      — .rpm headers
//   PUT    /packages/{filename}      — .rpm upload (parse + DB + FTS + Kick)
//   DELETE /packages/{filename}      — soft-delete via trash
package rpm

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// SeverityGateFn is the scan-severity gate hook. nil = no-op.
type SeverityGateFn func(ctx context.Context, repoID int64, artifactKind, artifactID string) (blocked bool, severity string, scanID int64)

// Deps bundles the dependencies the RPM handler needs.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo

	RPMPackages    *metadata.RPMPackagesRepo
	SigningKeys    *metadata.SigningKeysRepo
	Scans          *metadata.ScansRepo
	Coalescer      *regen.Registry
	PublicKeyCache *PublicKeyCache

	Path  storage.PathStore
	Trash storage.Trash
	Audit audit.Logger

	SeverityGate SeverityGateFn

	MaxPutBytes int64
	RepoRoot    string
}

// Handler serves the RPM protocol surface.
type Handler struct {
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	sessions *metadata.SessionsRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo

	rpmPackages    *metadata.RPMPackagesRepo
	signingKeys    *metadata.SigningKeysRepo
	scans          *metadata.ScansRepo
	coalescer      *regen.Registry
	publicKeyCache *PublicKeyCache

	pathStore    storage.PathStore
	trash        storage.Trash
	auditLogger  audit.Logger
	severityGate SeverityGateFn

	maxPutBytes int64
	repoRoot    string
}

// defaultMaxPutBytes is the spec-default 5 GiB cap.
const defaultMaxPutBytes = int64(5) << 30

// New constructs a Handler.
func New(d Deps) *Handler {
	max := d.MaxPutBytes
	if max <= 0 {
		max = defaultMaxPutBytes
	}
	return &Handler{
		db:             d.DB,
		users:          d.Users,
		apiKeys:        d.APIKeys,
		sessions:       d.Sessions,
		repos:          d.Repos,
		projects:       d.Projects,
		members:        d.Members,
		rpmPackages:    d.RPMPackages,
		signingKeys:    d.SigningKeys,
		scans:          d.Scans,
		coalescer:      d.Coalescer,
		publicKeyCache: d.PublicKeyCache,
		pathStore:      d.Path,
		trash:          d.Trash,
		auditLogger:    d.Audit,
		severityGate:   d.SeverityGate,
		maxPutBytes:    max,
		repoRoot:       d.RepoRoot,
	}
}

// Mount registers the RPM routes on parent. Mirrors the helm/raw middleware
// chain (AnonymousReadOK + skipIfActor(BasicOrAPIKey)).
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(h.lookupRepoPublicRead, h.extractRepoFromRPMURL, attachAnonymous))
		r.Use(skipIfActor(authmw.BasicOrAPIKey(midDeps)))

		r.Get("/{project}/rpm/{repo}/public-key.asc", h.servePublicKey)
		r.Get("/{project}/rpm/{repo}/repodata/*", h.serveRepodata)
		r.Get("/{project}/rpm/{repo}/packages/{filename}", h.servePackage)
		r.Head("/{project}/rpm/{repo}/packages/{filename}", h.servePackage)

		// Phase 8 Plan 01 (MIRROR-03): gate RPM write paths behind
		// MirrorGuardFixed so mirror repos reject uploads with 403
		// repo.repo_is_mirror.
		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "rpm"))
			rw.Put("/{project}/rpm/{repo}/packages/{filename}", h.put)
			rw.Delete("/{project}/rpm/{repo}/packages/{filename}", h.delete)
		})
	})
}

// attachAnonymous wires an anonymous Actor into ctx.
var attachAnonymous httpx.AttachAnonymousFn = func(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

// skipIfActor wraps a middleware so it pass-throughs when an Actor already
// sits in ctx (the anonymous fast path set by AnonymousReadOK).
func skipIfActor(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.ActorFromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

func (h *Handler) extractRepoFromRPMURL(r *http.Request) (project, repoType, repo string, ok bool) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(p, "/", 4)
	if len(parts) < 3 {
		return "", "", "", false
	}
	if parts[1] != "rpm" {
		return "", "", "", false
	}
	if parts[0] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], "rpm", parts[2], true
}

func (h *Handler) lookupRepoPublicRead(ctx context.Context, project, repoType, repo string) (bool, bool) {
	if h.projects == nil || h.repos == nil {
		return false, false
	}
	p, err := h.projects.FindByName(ctx, project)
	if err != nil || p == nil {
		return false, false
	}
	rr, err := h.repos.FindByTriple(ctx, p.ID, repoType, repo)
	if err != nil || rr == nil {
		return false, false
	}
	return rr.PublicRead, true
}

// resolved wraps a successful repo lookup.
type resolved struct {
	project  *metadata.Project
	repo     *metadata.Repo
	filename string
}

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request, requireFilename bool) (resolved, bool) {
	// Protocol routes use {project}; the /api/v1 session-authed shim
	// (F-06.3 row-delete) uses {name}. Fall back so one handler serves
	// both mount points.
	projectName := chi.URLParam(r, "project")
	if projectName == "" {
		projectName = chi.URLParam(r, "name")
	}
	repoName := chi.URLParam(r, "repo")
	filename := chi.URLParam(r, "filename")
	if dec, err := url.PathUnescape(filename); err == nil {
		filename = dec
	}

	if projectName == "" || repoName == "" {
		http.Error(w, "missing project or repo", http.StatusNotFound)
		return resolved{}, false
	}
	proj, err := h.projects.FindByName(r.Context(), projectName)
	if err != nil || proj == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return resolved{}, false
	}
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "rpm", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}
	if requireFilename {
		cleaned, perr := validateFilename(filename)
		if perr != nil {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return resolved{}, false
		}
		filename = cleaned
	}
	return resolved{project: proj, repo: rr, filename: filename}, true
}

// validateFilename rejects path traversal, NUL bytes, and any filename
// containing a path separator. RPM packages live in a single packages/ dir.
func validateFilename(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty filename")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte")
	}
	if strings.ContainsAny(raw, "/\\") {
		return "", errors.New("filename must not contain separators")
	}
	if raw == "." || raw == ".." {
		return "", errors.New("invalid filename")
	}
	if path.Clean(raw) != raw {
		return "", errors.New("non-canonical filename")
	}
	if !strings.HasSuffix(raw, ".rpm") {
		return "", errors.New("filename must end in .rpm")
	}
	return raw, nil
}

// storageKeyFor builds the PathStore-relative key for a package file.
func storageKeyFor(project, repo, filename string) string {
	return strings.Join([]string{project, "rpm", repo, "packages", filename}, "/")
}

// auditEvent is a tiny helper around d.Audit.Record that fills actor + req
// fields uniformly. Best-effort: errors swallowed.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	if h.auditLogger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: "rpm_package",
		TargetID:   targetID,
		Outcome:    outcome,
		Details:    details,
		OccurredAt: time.Now().UTC(),
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		switch a.Kind {
		case auth.ActorKindUser:
			id := a.ID
			e.ActorUserID = &id
		case auth.ActorKindAPIKey:
			id := a.APIKeyID
			e.ActorAPIKeyID = &id
		}
	}
	_ = h.auditLogger.Record(r.Context(), e)
}

// actorIsProjectMember returns true when actor is a member of projectID
// (or super-admin / scoped api-key).
func (h *Handler) actorIsProjectMember(ctx context.Context, actor auth.Actor, projectID int64) bool {
	if actor.Kind == auth.ActorKindAnonymous {
		return false
	}
	if actor.IsSuperAdmin {
		return true
	}
	if actor.Kind == auth.ActorKindAPIKey && actor.ProjectScope != nil {
		return *actor.ProjectScope == projectID
	}
	if actor.ID == 0 {
		return false
	}
	var n int
	err := h.db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id=? AND user_id=?`,
		projectID, actor.ID,
	).Scan(&n)
	if err != nil {
		return false
	}
	return n > 0
}

// actorCanRead consults auth.Can for ActionRepoRead.
func (h *Handler) actorCanRead(r *http.Request, repo *metadata.Repo) bool {
	a, ok := auth.ActorFromContext(r.Context())
	if !ok {
		return false
	}
	ctx := auth.ResolveMembership(r.Context(), a, h.members)
	allowed, _ := auth.Can(ctx, a, auth.ActionRepoRead, auth.Target{
		Kind:       "repo",
		ProjectID:  repo.ProjectID,
		RepoID:     repo.ID,
		PublicRead: repo.PublicRead,
	})
	return allowed
}
