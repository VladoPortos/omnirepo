package git

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/git/gitkit"
	"github.com/dxc-internal/omnirepo/internal/protocol/git/gogit"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// Deps bundles the dependencies the Git handler needs.
type Deps struct {
	Backend  GitServer
	Config   config.Config
	Locks    storage.Locks
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo
	Audit    audit.Logger
	DataRoot string

	// Auth deps for BasicOrAPIKey middleware.
	Users    *metadata.UsersRepo
	Sessions *metadata.SessionsRepo
	APIKeys  *metadata.APIKeysRepo

	// Plan 10: refs walker + repo lifecycle deps.
	DB   *metadata.DB
	Refs *metadata.GitRefsRepo
}

// Handler serves the Git Smart-HTTP protocol surface.
type Handler struct {
	backend  GitServer
	cfg      config.Config
	locks    storage.Locks
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo
	audit    audit.Logger
	dataRoot string

	users    *metadata.UsersRepo
	sessions *metadata.SessionsRepo
	apiKeys  *metadata.APIKeysRepo

	db   *metadata.DB
	refs *metadata.GitRefsRepo
}

// New constructs a Handler.
func New(d Deps) *Handler {
	return &Handler{
		backend:  d.Backend,
		cfg:      d.Config,
		locks:    d.Locks,
		repos:    d.Repos,
		projects: d.Projects,
		members:  d.Members,
		audit:    d.Audit,
		dataRoot: d.DataRoot,
		users:    d.Users,
		sessions: d.Sessions,
		apiKeys:  d.APIKeys,
		db:       d.DB,
		refs:     d.Refs,
	}
}

// Mount registers the Git Smart-HTTP routes on parent. The middleware chain
// follows D-30 order:
//
//  1. BasicOrAPIKey — auth (never falls back to anon)
//  2. ResolveRepoFromURL — parse URL, look up project + repo
//  3. RequireGitPermission — derive action from path, check auth.Can
//  4. PerRepoMutex — no-op on reads; serialize writes per repo
//  5. PushSizeLimit — MaxBytesReader cap on wire bytes (D-33)
//  6. AuditMiddleware — defer-style capture
//
// Two URL shapes are mounted with the same handler chain:
//   - "/git/{project}/{repo}"   — legacy form (kept for compatibility)
//   - "/{project}/git/{repo}"   — canonical form, matches every other
//     protocol's "/{project}/{proto}/{repo}/..." layout.
//
// Plan 11 will wrap ReceivePack completion with a post-hook for refs walker.
func (h *Handler) Mount(parent chi.Router) {
	authDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
		Projects: h.projects,
	}
	h.mountAt(parent, "/git/{project}/{repo}", authDeps)
	h.mountAt(parent, "/{project}/git/{repo}", authDeps)
}

// mountAt installs the full Git Smart-HTTP middleware chain at route on
// parent. Extracted so Mount can install both the legacy and canonical
// URL shapes without duplicating the chain.
func (h *Handler) mountAt(parent chi.Router, route string, authDeps authmw.Deps) {
	parent.Route(route, func(r chi.Router) {
		r.Use(authmw.BasicOrAPIKey(authDeps))
		r.Use(resolveMembership(h.members))
		r.Use(ResolveRepoFromURL(h.projects, h.repos))
		r.Use(RequireGitPermission())
		r.Use(PerRepoMutex(h.locks))
		r.Use(PushSizeLimit(ResolveMaxPushBytes(h.cfg.Repos.Git.MaxPushBytes)))
		// Plan 11 refs-walker post-hook will be inserted here.
		r.Handle("/*", http.HandlerFunc(h.dispatchToBackend))
	})
}

// dispatchToBackend resolves the on-disk repo path from the context-stashed
// repo row and delegates to the configured GitServer backend.
//
// Post-ReceivePack hook (D-37): after the backend returns for a receive-pack
// POST, invoke WalkAndReplace to sync git_refs while the PerRepoMutex is
// still held (the mutex defer-Unlock is higher in the call stack). Walker
// errors are logged but do NOT fail the push response — best-effort.
func (h *Handler) dispatchToBackend(w http.ResponseWriter, r *http.Request) {
	repo := RepoFromContext(r.Context())
	if repo == nil {
		http.NotFound(w, r)
		return
	}

	projectName := projectFromContext(r.Context())
	repoPath := filepath.Join(h.dataRoot, "repos", projectName, "git", repo.Name+".git")

	isReceivePack := r.Method == http.MethodPost &&
		strings.HasSuffix(r.URL.Path, "/git-receive-pack")

	h.backend.Handler(repoPath).ServeHTTP(w, r)

	// Post-receive-pack: sync git_refs while mutex is still held.
	if isReceivePack && h.db != nil && h.refs != nil {
		if err := WalkAndReplace(r.Context(), h.db, h.refs, repo.ID, repoPath); err != nil {
			slog.WarnContext(r.Context(), "git.refs.sync failed", "err", err, "repo_id", repo.ID)
		} else {
			// Emit audit event with ref count.
			refs, _ := h.refs.List(r.Context(), repo.ID)
			if h.audit != nil {
				_ = h.audit.Record(r.Context(), audit.Event{
					Kind:       audit.EvtGitRefsSynced,
					TargetKind: "repo",
					TargetID:   repo.Name,
					Details: map[string]any{
						"repo_id":   repo.ID,
						"ref_count": len(refs),
						"project":   projectName,
					},
				})
			}
		}
	}
}

// SelectBackend returns the GitServer implementation selected by config.
// "gogit" (default) returns the pure-Go go-git v6 backend; "gitkit" returns
// the subprocess-based fallback. Called once at boot time.
func SelectBackend(cfg config.Config) GitServer {
	switch cfg.Server.GitBackend {
	case "gitkit":
		return gitkit.New()
	default:
		return gogit.New()
	}
}

// TestRouter returns a chi.Mux wired with a simplified middleware chain
// (no auth) for integration tests. The handler routes resolve the repo
// from URL, apply the per-repo mutex, and dispatch to the backend with
// the post-receive walker hook. Auth is bypassed: every request is treated
// as an authenticated super-admin.
func (h *Handler) TestRouter(t testing.TB) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	mountTest := func(route string) {
		r.Route(route, func(sub chi.Router) {
			sub.Use(ResolveRepoFromURL(h.projects, h.repos))
			sub.Use(PerRepoMutex(h.locks))
			sub.Handle("/*", http.HandlerFunc(h.dispatchToBackend))
		})
	}
	mountTest("/git/{project}/{repo}")
	mountTest("/{project}/git/{repo}")
	return r
}
