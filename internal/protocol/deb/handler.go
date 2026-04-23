// Package deb — HTTP handler. Mounts the APT/Debian protocol surface at
// /<project>/deb/<repo>/... per APT-01..05.
//
// Routes:
//
//	GET    /public-key.asc                            — armored public key
//	GET    /dists/*                                   — InRelease/Release/Packages*
//	GET    /pool/*                                    — .deb download (severity-gated)
//	HEAD   /pool/*                                    — .deb headers
//	PUT    /pool/*                                    — .deb upload (parse + DB + FTS + Kick)
//	DELETE /pool/*                                    — trash move + DB drop + Kick
//	PATCH  /suites                                    — admin: extend apt_suites matrix
package deb

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

// Deps bundles the dependencies the DEB handler needs.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo

	DEBPackages    *metadata.DEBPackagesRepo
	AptSuites      *metadata.AptSuitesRepo
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

// defaultMaxPutBytes is the spec-default 5 GiB cap.
const defaultMaxPutBytes = int64(5) << 30

// Handler serves the DEB protocol surface.
type Handler struct {
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	sessions *metadata.SessionsRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo

	debPackages    *metadata.DEBPackagesRepo
	aptSuites      *metadata.AptSuitesRepo
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
		debPackages:    d.DEBPackages,
		aptSuites:      d.AptSuites,
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

// Mount registers the DEB routes on parent. Mirrors the RPM middleware chain.
//
// Phase 8 Plan 01 (MIRROR-03): PUT /{project}/deb/{repo}/pool/* is wrapped
// in httpx.MirrorGuardFixed so mirror-flagged repos reject upload attempts
// with 403 repo.repo_is_mirror. Read paths (dists, pool GET/HEAD) are
// intentionally NOT gated — mirror repos are still publicly readable.
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(h.lookupRepoPublicRead, h.extractRepoFromDEBURL, attachAnonymous))
		r.Use(skipIfActor(authmw.BasicOrAPIKey(midDeps)))

		r.Get("/{project}/deb/{repo}/public-key.asc", h.servePublicKey)
		r.Get("/{project}/deb/{repo}/dists/*", h.serveDistsFile)
		r.Get("/{project}/deb/{repo}/pool/*", h.servePoolPackage)
		r.Head("/{project}/deb/{repo}/pool/*", h.servePoolPackage)

		// Write paths gated behind the mirror guard.
		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "deb"))
			rw.Put("/{project}/deb/{repo}/pool/*", h.put)
			rw.Delete("/{project}/deb/{repo}/pool/*", h.delete)
			rw.Patch("/{project}/deb/{repo}/suites", h.patchSuites)
		})
	})
}

// attachAnonymous wires an anonymous Actor into ctx.
var attachAnonymous httpx.AttachAnonymousFn = func(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

// skipIfActor wraps a middleware so it passes through when an Actor already
// sits in ctx.
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

func (h *Handler) extractRepoFromDEBURL(r *http.Request) (project, repoType, repo string, ok bool) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(p, "/", 4)
	if len(parts) < 3 {
		return "", "", "", false
	}
	if parts[1] != "deb" {
		return "", "", "", false
	}
	if parts[0] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], "deb", parts[2], true
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

// resolved wraps a successful repo lookup. `rest` is the chi wildcard.
type resolved struct {
	project *metadata.Project
	repo    *metadata.Repo
	rest    string // wildcard tail (pool/... or dists/...)
}

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (resolved, bool) {
	// Protocol routes use {project}; the /api/v1 session-authed shim
	// (F-06.3 row-delete) uses {name}. Fall back so one handler serves
	// both mount points.
	projectName := chi.URLParam(r, "project")
	if projectName == "" {
		projectName = chi.URLParam(r, "name")
	}
	repoName := chi.URLParam(r, "repo")
	if projectName == "" || repoName == "" {
		http.Error(w, "missing project or repo", http.StatusNotFound)
		return resolved{}, false
	}
	proj, err := h.projects.FindByName(r.Context(), projectName)
	if err != nil || proj == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return resolved{}, false
	}
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "deb", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}
	rest := chi.URLParam(r, "*")
	if dec, err := url.PathUnescape(rest); err == nil {
		rest = dec
	}
	return resolved{project: proj, repo: rr, rest: rest}, true
}

// validatePoolSubpath enforces the pool layout:
//
//	pool/<c>/<package>/<filename>.deb
//
// Where <c> is one of 0-9, a-z, "lib<x>", etc. We reject traversal, NUL bytes,
// non-canonical, and require the final segment to end in ".deb".
func validatePoolSubpath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte")
	}
	p := strings.TrimPrefix(raw, "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid segment")
		}
	}
	cleaned := path.Clean("/" + p)
	if strings.HasPrefix(cleaned, "/..") {
		return "", errors.New("path escape")
	}
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned != p {
		return "", errors.New("non-canonical")
	}
	if !strings.HasSuffix(cleaned, ".deb") {
		return "", errors.New("not a .deb")
	}
	return cleaned, nil
}

// validateDistsSubpath mirrors validatePoolSubpath but allows any file name
// (InRelease, Release, Release.gpg, Packages, Packages.gz).
func validateDistsSubpath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte")
	}
	p := strings.TrimPrefix(raw, "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid segment")
		}
	}
	cleaned := path.Clean("/" + p)
	if strings.HasPrefix(cleaned, "/..") {
		return "", errors.New("path escape")
	}
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned != p {
		return "", errors.New("non-canonical")
	}
	return cleaned, nil
}

// storageKeyForPool builds the PathStore-relative key for a pool file.
// `rest` already starts with "pool/..." (the wildcard keeps the prefix).
func storageKeyForPool(project, repo, rest string) string {
	return strings.Join([]string{project, "deb", repo, rest}, "/")
}

// auditEvent emits an audit row with actor + request fields filled in.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	if h.auditLogger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: "deb_package",
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
// (super-admin / scoped api-key also pass).
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
