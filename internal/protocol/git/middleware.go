// Package git implements the middleware chain for the Git Smart-HTTP surface.
//
// The chain runs in this order on every /git/... request:
//
//  1. BasicOrAPIKey — auth (handled upstream by the caller mounting this)
//  2. ResolveRepoFromURL — parse URL, look up project + repo, stash on ctx
//  3. RequireGitPermission — read action from URL path, check auth.Can
//  4. PerRepoMutex — write-path-only per-repo lock
//  5. PushSizeLimit — MaxBytesReader cap — see pushcap.go
//  6. Audit — defer-style capture of method/status/bytes
//
// Steps 2-4 + 6 live here; step 5 is in pushcap.go.
package git

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/storage"
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

// ---- Step 1b: resolveMembership ----

// resolveMembership populates auth.WithProjectMembership on the context so
// that downstream auth.Can checks can verify project membership. Without
// this, isMemberOfProject always returns false and all non-super-admin
// requests get 403.
func resolveMembership(members *metadata.MembersRepo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if actor, ok := auth.ActorFromContext(r.Context()); ok {
				r = r.WithContext(auth.ResolveMembership(r.Context(), actor, members))
			}
			next.ServeHTTP(w, r)
		})
	}
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
				writeMissingOrChallenge(w, r)
				return
			}

			proj, err := projects.FindByName(r.Context(), projectName)
			if err != nil {
				writeMissingOrChallenge(w, r)
				return
			}

			repo, err := repos.FindByTriple(r.Context(), proj.ID, "git", repoName)
			if err != nil {
				writeMissingOrChallenge(w, r)
				return
			}

			ctx := WithRepo(r.Context(), repo)
			ctx = WithProject(ctx, projectName)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeMissingOrChallenge keeps missing and private repos indistinguishable
// from anonymous callers. Authenticated callers
// are entitled to a real 404; unauthenticated ones get the same Basic
// challenge they would receive on a private-but-existing repo, so an
// attacker cannot enumerate repo names by status-code sniffing.
func writeMissingOrChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="omnirepo"`)
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	http.NotFound(w, r)
}

// ---- Step 3: RequireGitPermission ----

// RequireGitPermission returns a chi middleware that inspects the request
// URL path and query to determine whether the operation is a read
// (upload-pack / info/refs?service=git-upload-pack) or a write
// (receive-pack / info/refs?service=git-receive-pack), then calls
// auth.Can with the corresponding Action.
//
// Anonymous requests (no actor on ctx) are allowed to fall into auth.Can
// as Actor{Kind: ActorKindAnonymous}: for a read action on a repo with
// PublicRead=true the policy short-circuit returns allowed; every other
// path still 401s after Can rejects with ReasonRequiresAuth.
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
				actor = auth.Actor{Kind: auth.ActorKindAnonymous}
			}

			action := resolveGitAction(r)

			allowed, reason := auth.Can(r.Context(), actor, action,
				auth.Target{
					Kind:       "repo",
					ProjectID:  repo.ProjectID,
					RepoID:     repo.ID,
					PublicRead: repo.PublicRead,
				})
			if !allowed {
				// Distinguish "no creds" from "wrong creds" so git clients
				// get a 401 + WWW-Authenticate challenge they can respond
				// to, rather than a flat 403.
				if actor.Kind == auth.ActorKindAnonymous &&
					reason == auth.ReasonRequiresAuth {
					w.Header().Set("WWW-Authenticate", `Basic realm="omnirepo"`)
					http.Error(w, "unauthenticated", http.StatusUnauthorized)
					return
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// AnonymousGitRead returns a chi middleware that attaches an anonymous
// Actor to the request context when (a) no Authorization header is
// present, (b) the resolved repo has PublicRead=true, and (c) the URL
// describes a read operation (upload-pack, info/refs?service=upload-pack).
// Downstream auth middleware (wrapped in skipIfActor) then passes through
// without demanding credentials, and RequireGitPermission calls auth.Can
// with the anonymous actor — which the policy grants via the
// anonymous_public_read branch. Writes and non-public repos fall through
// unchanged so BasicOrAPIKey can 401.
//
// Must run AFTER ResolveRepoFromURL (to know repo.PublicRead) and BEFORE
// BasicOrAPIKey (which would otherwise 401 on missing auth).
func AnonymousGitRead() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				next.ServeHTTP(w, r)
				return
			}
			repo := RepoFromContext(r.Context())
			if repo == nil || !repo.PublicRead {
				next.ServeHTTP(w, r)
				return
			}
			if resolveGitAction(r) != auth.ActionGitRepoRead {
				next.ServeHTTP(w, r)
				return
			}
			ctx := auth.WithActor(r.Context(), auth.Actor{Kind: auth.ActorKindAnonymous})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// skipIfActor wraps mw so it pass-throughs when an Actor is already on
// ctx (the anonymous fast path set by AnonymousGitRead). Matches the
// pattern used by pypi/rpm/deb handlers.
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
// (receive-pack) on a per-repo mutex. Read operations (upload-pack,
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

// ---- Step 8: Audit ----

// GitAuditRecorder receives one callback per completed Git HTTP request
// with the response status and bytes written. *Handler implements it by
// emitting git.fetch audit events for completed upload-pack POSTs.
// Decoupled from the full audit.Logger to keep tests simple.
type GitAuditRecorder interface {
	RecordGitRequest(r *http.Request, status int, written int64)
}

// AuditMiddleware returns a chi middleware that captures the response
// status code and written bytes for every Git request and forwards them —
// together with the request — to rec after the inner handler completes.
func AuditMiddleware(rec GitAuditRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			rec.RecordGitRequest(r, sw.status, sw.written)
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

// Flush forwards to the underlying writer when it supports it — the git
// smart-HTTP backend flushes pack data incrementally during clones, and
// swallowing Flush here would buffer large transfers.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer for http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
