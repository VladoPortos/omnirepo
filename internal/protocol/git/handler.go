package git

import (
	"net/http"
	"path/filepath"

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
// Plan 11 will wrap ReceivePack completion with a post-hook for refs walker.
func (h *Handler) Mount(parent chi.Router) {
	authDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
		Projects: h.projects,
	}

	parent.Route("/git/{project}/{repo}", func(r chi.Router) {
		r.Use(authmw.BasicOrAPIKey(authDeps))
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
func (h *Handler) dispatchToBackend(w http.ResponseWriter, r *http.Request) {
	repo := RepoFromContext(r.Context())
	if repo == nil {
		http.NotFound(w, r)
		return
	}

	projectName := projectFromContext(r.Context())
	repoPath := filepath.Join(h.dataRoot, "repos", projectName, "git", repo.Name+".git")

	h.backend.Handler(repoPath).ServeHTTP(w, r)
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
