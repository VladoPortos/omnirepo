// Package httpx — SPA handler with index.html fallback.
//
// SPAHandler serves embedded web assets from the dist/ subdirectory of the
// provided embed.FS. Unknown paths fall back to index.html so React Router
// handles client-side routing. Paths that match a real file (JS, CSS,
// images, fonts) are served directly with proper Content-Type.
//
// Security: the handler only serves from the embedded FS (go:embed) so it
// cannot access the host filesystem.
package httpx

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/vladoportos/omnirepo/internal/httperr"
)

// isAPILikePath reports whether a NotFound request looks like a machine-
// consumer path that should 404 as JSON instead of falling through to the
// SPA shell. Without this guard, `GET /api/v1/missing-route` returns 200 +
// index.html which is deeply misleading to anyone debugging an API call.
func isAPILikePath(path string) bool {
	switch {
	case strings.HasPrefix(path, "/api/"),
		strings.HasPrefix(path, "/v2/"):
		return true
	}
	return false
}

// writeAPINotFound emits the canonical ApiErrorEnvelope for unknown
// /api/* or /v2/* routes so machine consumers get the same shape as
// every other /api/v1 error path. Class is validation (not
// transient) because a missing route will not succeed on retry — the
// UI renders a non-retryable alert rather than a Try again button.
func writeAPINotFound(w http.ResponseWriter, r *http.Request) {
	httperr.Write(w, r, httperr.Validation(
		"resource.not_found",
		"That route does not exist.",
		httperr.WithStatus(http.StatusNotFound),
	))
}

// SPAHandler returns an http.HandlerFunc that serves the SPA assets from
// distFS. It expects distFS to contain a "dist" subdirectory with
// index.html at the root.
//
// Mount as the chi.Router NotFound handler AFTER all API/protocol routes
// so unknown paths serve the SPA shell.
func SPAHandler(distFS fs.FS) http.HandlerFunc {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("spa: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	return func(w http.ResponseWriter, r *http.Request) {
		// Never serve the SPA for /api/* or /v2/* NotFound requests — those
		// reach here only when every mounted route has passed, so the right
		// answer is a structured 404 JSON response.
		if isAPILikePath(r.URL.Path) {
			writeAPINotFound(w, r)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Directory request (e.g. /swagger/) — try to serve <dir>/index.html
		// directly. Without this branch http.FileServer's directory-redirect
		// to "./" lands in the SPA fallback below for embedded subapps that
		// ship their own index.html (Swagger UI). We don't want to override
		// that with the React app's shell.
		statPath := strings.TrimSuffix(path, "/")
		if path != "" && strings.HasSuffix(r.URL.Path, "/") {
			if _, err := fs.Stat(sub, statPath+"/index.html"); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Try serving the exact file first.
		if _, err := fs.Stat(sub, statPath); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for client-side routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}
