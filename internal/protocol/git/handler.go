package git

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	authmw "github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/httperr"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/git/gitkit"
	"github.com/vladoportos/omnirepo/internal/protocol/git/gogit"
	"github.com/vladoportos/omnirepo/internal/storage"
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

	// Refs walker + repo lifecycle deps.
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
// order allows anonymous clones of public_read=true repos:
//
//  1. ResolveRepoFromURL — parse URL, look up project + repo
//  2. AuditMiddleware — emits a git.fetch audit event per completed
//     upload-pack POST, success or denial (it wraps the rest of the
//     chain so 401/403 fetch attempts are audited with outcome=error;
//     pushes are audited via EvtGitRefsSynced in dispatchToBackend).
//  3. AnonymousGitRead   — when no Authorization header is present and the
//     repo is public_read + the action is a read, attach an anonymous actor
//     so downstream checks pass; otherwise fall through.
//  4. skipIfActor(BasicOrAPIKey) — auth path for credentialed clients;
//     skipped when step 3 already attached anonymous.
//  5. resolveMembership — fills project-membership cache for auth.Can.
//  6. RequireGitPermission — derive action from path, check auth.Can.
//  7. PerRepoMutex — no-op on reads; serialize writes per repo.
//  8. PushSizeLimit — MaxBytesReader cap on wire bytes.
//
// Two URL shapes are mounted with the same handler chain:
//   - "/git/{project}/{repo}"   — legacy form (kept for compatibility)
//   - "/{project}/git/{repo}"   — canonical form, matches every other
//     protocol's "/{project}/{proto}/{repo}/..." layout.
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
		r.Use(ResolveRepoFromURL(h.projects, h.repos))
		r.Use(AuditMiddleware(h))
		r.Use(AnonymousGitRead())
		r.Use(skipIfActor(authmw.BasicOrAPIKey(authDeps)))
		r.Use(resolveMembership(h.members))
		r.Use(RequireGitPermission())
		r.Use(PerRepoMutex(h.locks))
		r.Use(PushSizeLimit(ResolveMaxPushBytes(h.cfg.Repos.Git.MaxPushBytes)))
		// LFS batch endpoint returns 501 lfs.not_supported. Registered
		// BEFORE the /* catch-all so chi's specific-pattern-beats-wildcard
		// precedence wins — applies to every method (see lfs.go for rationale).
		r.Handle("/info/lfs/objects/batch", http.HandlerFunc(h.rejectLFS))
		r.Handle("/*", http.HandlerFunc(h.dispatchToBackend))
	})
}

// dispatchToBackend resolves the on-disk repo path from the context-stashed
// repo row and delegates to the configured GitServer backend.
//
// Post-ReceivePack hook: after the backend returns for a receive-pack
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

	// Receive-pack against mirror repos is rejected with 403 + httperr
	// envelope code `mirror.push_rejected`. The gate covers BOTH the
	// POST /git-receive-pack (actual push) AND the GET
	// /info/refs?service=git-receive-pack (capability negotiation) paths.
	// Gating only the POST would let clients negotiate push and read the full
	// ref snapshot before the 403 — the info/refs leg is where pkt-line
	// advertises every ref, so we reject it too.
	//
	// Git clients surface the 403 as "fatal: ... The requested URL returned
	// error: 403". The JSON envelope is for operators hitting the endpoint
	// directly via curl/Postman — a JSON envelope via httperr.Write is the
	// portable answer for Smart-HTTP refusal.
	isReceivePackGet := r.Method == http.MethodGet &&
		strings.HasSuffix(r.URL.Path, "/info/refs") &&
		r.URL.Query().Get("service") == "git-receive-pack"
	if (isReceivePack || isReceivePackGet) && repo.IsMirror {
		httperr.Write(w, r, httperr.Permission(
			"mirror.push_rejected",
			"Push is not allowed on mirror repositories.",
		))
		return
	}

	// Mirror not-yet-synced 503 envelope.
	// OnRepoCreate intentionally skips InitBare for mirror repos
	// because gogit.PlainCloneContext on the first /sync requires an empty
	// target dir. Until the first sync runs, <repoPath>/HEAD does not exist
	// — a clone attempt against an unsynced mirror used to fall through to
	// the backend and emit a cryptic go-git error (or a half-initialised
	// HTML 500 from gitkit). We catch that case here and return a clean
	// 503 + httperr envelope so operators / scripted clients see actionable
	// JSON instead of backend internals. Non-mirror repos always have
	// InitBare run by OnRepoCreate, so a missing dir for them is a genuine
	// internal/ops issue — let it propagate as before.
	if repo.IsMirror {
		if _, statErr := os.Stat(filepath.Join(repoPath, "HEAD")); errors.Is(statErr, os.ErrNotExist) {
			httperr.Write(w, r, httperr.Transient(
				"mirror.not_yet_synced",
				"Mirror repository has not been synced yet. Trigger a sync to populate.",
				0,
			))
			return
		}
	}

	h.backend.Handler(repoPath).ServeHTTP(w, r)

	// Post-receive-pack: sync git_refs while mutex is still held.
	if isReceivePack && h.db != nil && h.refs != nil {
		if err := WalkAndReplace(r.Context(), h.db, h.refs, repo.ID, repoPath); err != nil {
			slog.WarnContext(r.Context(), "git.refs.sync failed", "err", err, "repo_id", repo.ID)
		} else {
			// Emit audit event with ref count.
			refs, _ := h.refs.List(r.Context(), repo.ID)
			if h.audit != nil {
				ev := audit.Event{
					Kind:       audit.EvtGitRefsSynced,
					TargetKind: "repo",
					TargetID:   repo.Name,
					Details: map[string]any{
						"repo_id":   repo.ID,
						"ref_count": len(refs),
						"project":   projectName,
					},
				}
				// dispatchToBackend is the innermost handler, so auth has
				// already stored the actor on the request context.
				if actor, ok := auth.ActorFromContext(r.Context()); ok {
					applyGitActor(&ev, actor)
				}
				_ = h.audit.Record(r.Context(), ev)
			}
		}
	}
}

// RecordGitRequest implements GitAuditRecorder. It emits one git.fetch
// audit event per completed upload-pack POST (the actual clone/fetch pack
// transfer). info/refs advertisements are skipped so a single clone does
// not double-log; pushes are audited separately via EvtGitRefsSynced in
// dispatchToBackend.
func (h *Handler) RecordGitRequest(r *http.Request, status int, written int64) {
	if h.audit == nil || r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
		return
	}
	repo := RepoFromContext(r.Context())
	if repo == nil {
		return
	}
	outcome := "ok"
	if status >= 400 {
		outcome = "error"
	}
	ev := audit.Event{
		Kind:       audit.EvtGitFetch,
		TargetKind: "repo",
		TargetID:   repo.Name,
		Outcome:    outcome,
		Details: map[string]any{
			"repo_id": repo.ID,
			"project": projectFromContext(r.Context()),
			"status":  status,
			"bytes":   written,
		},
	}
	// This runs in AuditMiddleware, which wraps auth, so r.Context() here does
	// not carry the actor directly — read it from the actor box the middleware
	// seeded (auth's WithActor filled it from inside the chain).
	if box := auth.ActorBoxFromContext(r.Context()); box != nil {
		applyGitActor(&ev, box.Actor)
	}
	_ = h.audit.Record(r.Context(), ev)
}

// applyGitActor attributes ev to the authenticated actor. Anonymous and
// zero-value actors leave the actor fields nil, so the event stays anonymous.
func applyGitActor(ev *audit.Event, actor auth.Actor) {
	switch actor.Kind {
	case auth.ActorKindUser:
		id := actor.ID
		ev.ActorUserID = &id
	case auth.ActorKindAPIKey:
		id := actor.APIKeyID
		ev.ActorAPIKeyID = &id
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
			// Mirror the production mountAt ordering: LFS refusal registered
			// BEFORE the /* catch-all so the specific-pattern-beats-wildcard
			// precedence wins inside the test harness too.
			sub.Handle("/info/lfs/objects/batch", http.HandlerFunc(h.rejectLFS))
			sub.Handle("/*", http.HandlerFunc(h.dispatchToBackend))
		})
	}
	mountTest("/git/{project}/{repo}")
	mountTest("/{project}/git/{repo}")
	return r
}
