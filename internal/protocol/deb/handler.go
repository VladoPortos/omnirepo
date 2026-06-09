// Package deb — HTTP handler. Mounts the APT/Debian protocol surface at
// /<project>/deb/<repo>/...
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

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	authmw "github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/common"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// SeverityGateFn is the scan-severity gate hook. nil = no-op.
type SeverityGateFn = common.SeverityGateFn

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
// PUT /{project}/deb/{repo}/pool/* is wrapped in httpx.MirrorGuardFixed so
// mirror-flagged repos reject upload attempts with 403 repo.repo_is_mirror.
// Read paths (dists, pool GET/HEAD) are intentionally NOT gated — mirror
// repos are still publicly readable.
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(common.RepoPublicReadLookup(h.projects, h.repos), common.RepoURLExtractor("deb"), common.AttachAnonymous))
		r.Use(common.SkipIfActor(authmw.BasicOrAPIKey(midDeps)))

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

// resolved wraps a successful repo lookup. `rest` is the chi wildcard.
type resolved struct {
	project *metadata.Project
	repo    *metadata.Repo
	rest    string // wildcard tail (pool/... or dists/...)
}

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (resolved, bool) {
	// Protocol routes use {project}; the /api/v1 session-authed shim
	// (row-delete) uses {name}. Fall back so one handler serves
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

// auditEvent records a deb_package audit event via common.AuditEvent.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	common.AuditEvent(h.auditLogger, r, kind, "deb_package", targetID, outcome, details)
}

// requireRepoWrite enforces the maintainer-required policy for artifact
// writes/deletes (see common.RequireRepoWrite).
func (h *Handler) requireRepoWrite(ctx context.Context, actor auth.Actor, projectID int64, action auth.Action) bool {
	return common.RequireRepoWrite(ctx, actor, h.members, projectID, action)
}

// actorCanRead consults auth.Can for ActionRepoRead (see common.ActorCanRead).
func (h *Handler) actorCanRead(r *http.Request, repo *metadata.Repo) bool {
	return common.ActorCanRead(r, h.members, repo)
}
