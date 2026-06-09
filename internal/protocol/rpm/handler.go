// Package rpm — HTTP handler. Mounts the RPM-protocol surface at
// /<project>/rpm/<repo>/...
//
// Routes:
//
//	GET    /public-key.asc           — armored public key (lock-free cache)
//	GET    /repodata/*               — repodata files (lock-free disk serve)
//	GET    /packages/{filename}      — .rpm download (severity-gated)
//	HEAD   /packages/{filename}      — .rpm headers
//	PUT    /packages/{filename}      — .rpm upload (parse + DB + FTS + Kick)
//	DELETE /packages/{filename}      — soft-delete via trash
package rpm

import (
	"context"
	"net/http"
	"net/url"
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
		r.Use(httpx.AnonymousReadOK(common.RepoPublicReadLookup(h.projects, h.repos), common.RepoURLExtractor("rpm"), common.AttachAnonymous))
		r.Use(common.SkipIfActor(authmw.BasicOrAPIKey(midDeps)))

		r.Get("/{project}/rpm/{repo}/public-key.asc", h.servePublicKey)
		r.Get("/{project}/rpm/{repo}/repodata/*", h.serveRepodata)
		r.Get("/{project}/rpm/{repo}/packages/{filename}", h.servePackage)
		r.Head("/{project}/rpm/{repo}/packages/{filename}", h.servePackage)

		// Gate RPM write paths behind MirrorGuardFixed so mirror repos
		// reject uploads with 403 repo.repo_is_mirror.
		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "rpm"))
			rw.Put("/{project}/rpm/{repo}/packages/{filename}", h.put)
			rw.Delete("/{project}/rpm/{repo}/packages/{filename}", h.delete)
		})
	})
}

// resolved wraps a successful repo lookup.
type resolved struct {
	project  *metadata.Project
	repo     *metadata.Repo
	filename string
}

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request, requireFilename bool) (resolved, bool) {
	// Protocol routes use {project}; the /api/v1 session-authed shim
	// (row-delete) uses {name}. Fall back so one handler serves
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
		cleaned, perr := common.ValidateFilename(filename, ".rpm")
		if perr != nil {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return resolved{}, false
		}
		filename = cleaned
	}
	return resolved{project: proj, repo: rr, filename: filename}, true
}

// storageKeyFor builds the PathStore-relative key for a package file.
func storageKeyFor(project, repo, filename string) string {
	return strings.Join([]string{project, "rpm", repo, "packages", filename}, "/")
}

// auditEvent records an rpm_package audit event via common.AuditEvent.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	common.AuditEvent(h.auditLogger, r, kind, "rpm_package", targetID, outcome, details)
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
