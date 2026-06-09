package helm

import (
	"context"
	"net/http"
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

// SeverityGateFn is the scan-severity gate hook. Plug nil for no-op (tests);
// app.Run wires a real gate that inspects repos.block_on_severity + scans.
type SeverityGateFn = common.SeverityGateFn

// Deps is the dependency bundle the Helm handler needs at construction time.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo

	HelmCharts *metadata.HelmChartsRepo
	Scans      *metadata.ScansRepo
	Coalescer  *regen.Registry

	Path  storage.PathStore
	Trash storage.Trash
	Audit audit.Logger

	SeverityGate SeverityGateFn

	MaxPutBytes int64
	RepoRoot    string
}

// Handler serves the Helm v3 chart-repository protocol surface. Constructed
// by New, mounted on a chi router via Mount.
type Handler struct {
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	sessions *metadata.SessionsRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo

	helmCharts *metadata.HelmChartsRepo
	scans      *metadata.ScansRepo
	coalescer  *regen.Registry

	pathStore    storage.PathStore
	trash        storage.Trash
	auditLogger  audit.Logger
	severityGate SeverityGateFn

	maxPutBytes int64
	repoRoot    string
}

// defaultMaxPutBytes is the spec-default 5 GiB cap on a single PUT body.
const defaultMaxPutBytes = int64(5) << 30

// New constructs a Helm Handler from deps.
func New(d Deps) *Handler {
	max := d.MaxPutBytes
	if max <= 0 {
		max = defaultMaxPutBytes
	}
	return &Handler{
		db:           d.DB,
		users:        d.Users,
		apiKeys:      d.APIKeys,
		sessions:     d.Sessions,
		repos:        d.Repos,
		projects:     d.Projects,
		members:      d.Members,
		helmCharts:   d.HelmCharts,
		scans:        d.Scans,
		coalescer:    d.Coalescer,
		pathStore:    d.Path,
		trash:        d.Trash,
		auditLogger:  d.Audit,
		severityGate: d.SeverityGate,
		maxPutBytes:  max,
		repoRoot:     d.RepoRoot,
	}
}

// Mount registers the Helm routes on parent. Routes mirror the spec-defined
// surface at /<project>/helm/<repo>/... and honor AnonymousReadOK on GETs.
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(common.RepoPublicReadLookup(h.projects, h.repos), common.RepoURLExtractor("helm"), common.AttachAnonymous))
		r.Use(common.SkipIfActor(authmw.BasicOrAPIKey(midDeps)))

		// Downloads.
		r.Get("/{project}/helm/{repo}/index.yaml", h.getIndex)
		r.Get("/{project}/helm/{repo}/charts/{filename}", h.get)

		// Gate Helm write paths behind MirrorGuardFixed so mirror repos
		// reject uploads with 403 repo.repo_is_mirror. OCI-sourced mirrors
		// for Helm go through the OCI path which is guarded separately.
		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "helm"))
			// Upload .tgz and .prov — same PUT handler dispatches on filename.
			rw.Put("/{project}/helm/{repo}/charts/{filename}", h.put)
			rw.Delete("/{project}/helm/{repo}/charts/{filename}", h.delete)
		})
	})
}

// resolved wraps a successful project+repo lookup.
type resolved struct {
	project  *metadata.Project
	repo     *metadata.Repo
	filename string // bare filename (may end in .tgz or .tgz.prov)
}

// resolveRepo validates {project}+{repo} URL params, looks up the repo row,
// validates the filename (if requireFilename), and returns the resolved
// triple. Writes a 404/400 to w on miss and returns ok=false.
//
// Protocol routes use {project}; the /api/v1 session-authed shim
// (row-delete) uses {name}. Fall back so one handler serves
// both mount points.
func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request, requireFilename bool) (resolved, bool) {
	projectName := chi.URLParam(r, "project")
	if projectName == "" {
		projectName = chi.URLParam(r, "name")
	}
	repoName := chi.URLParam(r, "repo")
	filename := chi.URLParam(r, "filename")

	if projectName == "" || repoName == "" {
		http.Error(w, "missing project or repo", http.StatusNotFound)
		return resolved{}, false
	}
	proj, err := h.projects.FindByName(r.Context(), projectName)
	if err != nil || proj == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return resolved{}, false
	}
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "helm", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}
	if requireFilename {
		cleaned, perr := common.ValidateFilename(filename, "")
		if perr != nil {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return resolved{}, false
		}
		filename = cleaned
	}
	return resolved{project: proj, repo: rr, filename: filename}, true
}

// isProvenance reports whether the given filename is a chart provenance file
// (ends in .tgz.prov). Pass-through writes skip DB/FTS/coalescer.
func isProvenance(filename string) bool {
	return strings.HasSuffix(filename, ".tgz.prov") || strings.HasSuffix(filename, ".prov")
}

// isChartArchive reports whether the given filename looks like a chart .tgz.
func isChartArchive(filename string) bool {
	return strings.HasSuffix(filename, ".tgz") && !strings.HasSuffix(filename, ".tgz.prov")
}

// storageKeyFor builds the PathStore-relative key for a chart file.
func storageKeyFor(project, repo, filename string) string {
	return strings.Join([]string{project, "helm", repo, "charts", filename}, "/")
}

// auditEvent records a helm_chart audit event via common.AuditEvent.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	common.AuditEvent(h.auditLogger, r, kind, "helm_chart", targetID, outcome, details)
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
