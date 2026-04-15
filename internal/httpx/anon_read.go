package httpx

import (
	"context"
	"net/http"
)

// RepoLookupFn returns (publicRead, found) for the (project, repoType, repo)
// triple. Implementations typically wrap a *metadata.ReposRepo but the
// middleware itself stays DB-agnostic for testability.
type RepoLookupFn func(ctx context.Context, project, repoType, repo string) (publicRead bool, found bool)

// RepoExtractorFn parses the request URL and returns the (project, type, repo)
// triple + ok=true when the URL shape is a known repo-scoped read path for a
// given protocol. Each protocol supplies its own: /v2 uses one, RAW uses
// another. Returning ok=false means "this URL does not address a repo", and
// the middleware falls through without attempting an anonymous actor.
type RepoExtractorFn func(r *http.Request) (project, repoType, repo string, ok bool)

// AttachAnonymousFn is the callback the protocol handler supplies to attach
// its own anonymous Actor (of type auth.Actor, but opaque here to break an
// import cycle: auth already imports httpx for reserved-prefix validation).
// The caller invokes auth.WithActor internally.
type AttachAnonymousFn func(ctx context.Context) context.Context

// AnonymousReadOK is a chi middleware that grants an anonymous read path for
// public_read=true repos (D-32, D-33, REPO-09).
//
// Chain position: BEFORE the real auth middleware (BasicOrAPIKey etc.). On
// the anonymous branch this middleware attaches an anonymous Actor to the
// request ctx (via attach) and calls next. On the fall-through branch (auth
// header present, non-read method, URL not a repo, or repo not public), it
// simply calls next with the request untouched — the next middleware (real
// auth) decides whether to 401 or authenticate.
//
// The anonymous branch is restricted to:
//   - no Authorization header (leave creds'd requests to the real auth layer;
//     they might be a logged-in user whose permissions are strictly greater
//     than the anonymous-read grant, and we must NEVER downgrade an authn'd
//     actor to anonymous).
//   - HTTP method GET or HEAD (D-32 — reads only; anonymous writes/deletes
//     are policy-denied in Can but we also stop them at the edge).
//   - URL resolves to a known repo via extractor AND that repo is public_read.
func AnonymousReadOK(lookup RepoLookupFn, extractor RepoExtractorFn, attach AttachAnonymousFn) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			project, repoType, repo, ok := extractor(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			pub, found := lookup(r.Context(), project, repoType, repo)
			if !found || !pub {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(attach(r.Context())))
		})
	}
}
