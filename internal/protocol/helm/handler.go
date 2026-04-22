package helm

import (
	"context"
	"errors"
	"net/http"
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

// SeverityGateFn is the scan-severity gate hook. Plug nil for no-op (tests);
// app.Run wires a real gate that inspects repos.block_on_severity + scans.
type SeverityGateFn func(ctx context.Context, repoID int64, artifactKind, artifactID string) (blocked bool, severity string, scanID int64)

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
		r.Use(httpx.AnonymousReadOK(h.lookupRepoPublicRead, h.extractRepoFromHelmURL, attachAnonymous))
		r.Use(skipIfActor(authmw.BasicOrAPIKey(midDeps)))

		// Downloads.
		r.Get("/{project}/helm/{repo}/index.yaml", h.getIndex)
		r.Get("/{project}/helm/{repo}/charts/{filename}", h.get)

		// Phase 8 Plan 01 (MIRROR-03): gate Helm write paths behind
		// MirrorGuardFixed so mirror repos reject uploads with 403
		// repo.repo_is_mirror. OCI-sourced mirrors for Helm go through
		// the OCI path which is guarded separately.
		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "helm"))
			// Upload .tgz and .prov — same PUT handler dispatches on filename.
			rw.Put("/{project}/helm/{repo}/charts/{filename}", h.put)
			rw.Delete("/{project}/helm/{repo}/charts/{filename}", h.delete)
		})
	})
}

// attachAnonymous wires an anonymous Actor into ctx (used by AnonymousReadOK).
var attachAnonymous httpx.AttachAnonymousFn = func(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

// skipIfActor wraps a middleware so it pass-throughs when an Actor is already
// in ctx (the anonymous fast path set by AnonymousReadOK).
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

// extractRepoFromHelmURL returns (project, "helm", repo, ok=true) when the
// URL matches /<project>/helm/<repo>/.... Used by AnonymousReadOK.
func (h *Handler) extractRepoFromHelmURL(r *http.Request) (project, repoType, repo string, ok bool) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(p, "/", 4)
	if len(parts) < 3 {
		return "", "", "", false
	}
	if parts[1] != "helm" {
		return "", "", "", false
	}
	if parts[0] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], "helm", parts[2], true
}

// lookupRepoPublicRead resolves (project, "helm", repo) → (public_read, found).
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

// resolved wraps a successful project+repo lookup.
type resolved struct {
	project  *metadata.Project
	repo     *metadata.Repo
	filename string // bare filename (may end in .tgz or .tgz.prov)
}

// resolveRepo validates {project}+{repo} URL params, looks up the repo row,
// validates the filename (if requireFilename), and returns the resolved
// triple. Writes a 404/400 to w on miss and returns ok=false.
func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request, requireFilename bool) (resolved, bool) {
	projectName := chi.URLParam(r, "project")
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
		cleaned, perr := validateFilename(filename)
		if perr != nil {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return resolved{}, false
		}
		filename = cleaned
	}
	return resolved{project: proj, repo: rr, filename: filename}, true
}

// validateFilename rejects path traversal, NUL bytes, empty segments, and any
// filename containing a path separator. Helm charts live in a single charts/
// directory per repo — no nested layout is supported.
func validateFilename(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty filename")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte in filename")
	}
	if strings.ContainsAny(raw, "/\\") {
		return "", errors.New("filename must not contain separators")
	}
	if raw == "." || raw == ".." {
		return "", errors.New("invalid filename")
	}
	// Defense in depth: path.Clean should be a no-op.
	if path.Clean(raw) != raw {
		return "", errors.New("non-canonical filename")
	}
	return raw, nil
}

// isProvenance reports whether the given filename is a chart provenance file
// (ends in .tgz.prov). Pass-through writes (HELM-04) skip DB/FTS/coalescer.
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

// auditEvent is a tiny helper around d.Audit.Record that fills in actor +
// request fields uniformly. Best-effort: errors are swallowed.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	if h.auditLogger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: "helm_chart",
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

// actorIsProjectMember mirrors the raw handler's membership check: super-admin
// bypasses; project-scoped API keys match their own project; user actors are
// looked up via project_members.
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

// actorCanRead consults auth.Can for ActionRepoRead (CR-01). Populates ctx
// with project membership so private repos enforce it.
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
