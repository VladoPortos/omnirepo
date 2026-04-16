// Package git — middleware chain for the Git Smart-HTTP surface (D-30).
//
// The chain runs in this order on every /git/... request:
//
//  1. BasicOrAPIKey — auth (handled upstream by the caller mounting this)
//  2. ResolveRepoFromURL — parse URL, look up project + repo, stash on ctx
//  3. RequireGitPermission — read action from URL path, check auth.Can
//  4. PerRepoMutex — write-path-only per-repo lock (D-32)
//  5. PushSizeLimit — MaxBytesReader cap (D-33/34/35) — see pushcap.go
//  6. Audit — defer-style capture of method/status/bytes
//
// Plan 09 implements steps 2-4 + 6; step 5 is in pushcap.go.
package git

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// ---- context keys for repo stashing ----

type repoCtxKey struct{}

// WithRepo returns ctx annotated with a resolved Repo row. Middlewares and
// tests use this to stash the looked-up repo for downstream handlers.
func WithRepo(ctx context.Context, repo *metadata.Repo) context.Context {
	return context.WithValue(ctx, repoCtxKey{}, repo)
}

// RepoFromContext extracts the stashed *metadata.Repo. Returns nil when absent.
func RepoFromContext(ctx context.Context) *metadata.Repo {
	v := ctx.Value(repoCtxKey{})
	if v == nil {
		return nil
	}
	repo, _ := v.(*metadata.Repo)
	return repo
}

// projectCtxKey stashes the resolved project name for PerRepoMutex.
type projectCtxKey struct{}

// WithProject returns ctx annotated with the resolved project name.
// Exported for tests; production callers use ResolveRepoFromURL which
// sets this automatically.
func WithProject(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, projectCtxKey{}, name)
}

func projectFromContext(ctx context.Context) string {
	v, _ := ctx.Value(projectCtxKey{}).(string)
	return v
}

// ---- Step 2: ResolveRepoFromURL ----

// ResolveRepoFromURL returns a chi middleware that parses the URL path to
// extract {project} and {repo} chi params, looks up the project + repo in
// the DB, and stashes the *metadata.Repo on the context. On unknown
// project or repo, writes 404 without calling the inner handler.
//
// URL pattern: /git/{project}/{repo}.git/...
// The chi route param {repo} includes the ".git" suffix; this middleware
// strips it before the DB lookup.
func ResolveRepoFromURL(projects *metadata.ProjectsRepo, repos *metadata.ReposRepo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			projectName := chi.URLParam(r, "project")
			repoParam := chi.URLParam(r, "repo")

			// Strip .git suffix if present (URL is /{repo}.git/...).
			repoName := strings.TrimSuffix(repoParam, ".git")
			if projectName == "" || repoName == "" {
				http.NotFound(w, r)
				return
			}

			proj, err := projects.FindByName(r.Context(), projectName)
			if err != nil {
				http.NotFound(w, r)
				return
			}

			repo, err := repos.FindByTriple(r.Context(), proj.ID, "git", repoName)
			if err != nil {
				http.NotFound(w, r)
				return
			}

			ctx := WithRepo(r.Context(), repo)
			ctx = WithProject(ctx, projectName)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ---- Step 3: RequireGitPermission ----

// RequireGitPermission returns a chi middleware that inspects the request
// URL path and query to determine whether the operation is a read
// (upload-pack / info/refs?service=git-upload-pack) or a write
// (receive-pack / info/refs?service=git-receive-pack), then calls
// auth.Can with the corresponding Action.
func RequireGitPermission() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			repo := RepoFromContext(r.Context())
			if repo == nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			actor, ok := auth.ActorFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}

			action := resolveGitAction(r)

			allowed, _ := auth.Can(r.Context(), actor, action,
				auth.Target{Kind: "repo", ProjectID: repo.ProjectID, RepoID: repo.ID})
			if !allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// resolveGitAction determines ActionGitRepoRead or ActionGitRepoWrite from
// the request path and query string.
func resolveGitAction(r *http.Request) auth.Action {
	if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
		return auth.ActionGitRepoWrite
	}
	if strings.HasSuffix(r.URL.Path, "/info/refs") &&
		r.URL.Query().Get("service") == "git-receive-pack" {
		return auth.ActionGitRepoWrite
	}
	return auth.ActionGitRepoRead
}

// isReceivePackWrite returns true when the request is the actual POST to
// git-receive-pack (the write path that should hold a mutex and be size-capped).
// info/refs?service=git-receive-pack is NOT a write — it's the read-only
// capability negotiation that precedes a push.
func isReceivePackWrite(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack")
}

// ---- Step 4: PerRepoMutex ----

// PerRepoMutex returns a chi middleware that serializes write operations
// (receive-pack) on a per-repo mutex (D-32). Read operations (upload-pack,
// info/refs) pass through without locking.
func PerRepoMutex(locks storage.Locks) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isReceivePackWrite(r) {
				next.ServeHTTP(w, r)
				return
			}

			repo := RepoFromContext(r.Context())
			if repo == nil {
				next.ServeHTTP(w, r)
				return
			}

			projectName := projectFromContext(r.Context())
			if projectName == "" {
				projectName = chi.URLParam(r, "project")
			}

			key := storage.RepoKey{
				Project: projectName,
				Type:    "git",
				Repo:    repo.Name,
			}
			mu := locks.For(key)
			mu.Lock()
			defer mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
}

// ---- Step 6: Audit ----

// GitAuditLogger is the interface the audit middleware needs. Decoupled from
// the full audit.Logger to keep tests simple.
type GitAuditLogger interface {
	Record(method, path string, status int, bytes int64)
}

// AuditMiddleware returns a chi middleware that captures the HTTP method,
// path, response status code, and written bytes for every Git request.
func AuditMiddleware(logger GitAuditLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Record(r.Method, r.URL.Path, sw.status, sw.written)
		})
	}
}

// statusWriter wraps http.ResponseWriter to capture status + bytes written.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}
