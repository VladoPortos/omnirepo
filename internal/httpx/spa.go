// Package httpx — SPA handler with index.html fallback (UI-02).
//
// SPAHandler serves embedded web assets from the dist/ subdirectory of the
// provided embed.FS. Unknown paths fall back to index.html so React Router
// handles client-side routing. Paths that match a real file (JS, CSS,
// images, fonts) are served directly with proper Content-Type.
//
// Security: the handler only serves from the embedded FS (go:embed) so it
// cannot access the host filesystem (T-05-04-03).
package httpx

import (
	"io/fs"
	"net/http"
	"strings"
)

// isAPILikePath reports whether a NotFound request looks like a machine-
// consumer path that should 404 as JSON instead of falling through to the
// SPA shell. Without this guard, `GET /api/v1/missing-route` returns 200 +
// index.html which is deeply misleading to anyone debugging an API call
// (WALKTHROUGH-FINDINGS.md F-2a).
func isAPILikePath(path string) bool {
	switch {
	case strings.HasPrefix(path, "/api/"),
		strings.HasPrefix(path, "/v2/"):
		return true
	}
	return false
}

// writeAPINotFound emits the same error envelope api/errors.go uses
// (`{"error": "...", "detail": "..."}`) so clients get a consistent shape.
func writeAPINotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not_found","detail":"route not found"}`))
}

// SPAHandler returns an http.HandlerFunc that serves the SPA assets from
// distFS. It expects distFS to contain a "dist" subdirectory with
// index.html at the root.
//
// Mount as the chi.Router NotFound handler AFTER all API/protocol routes
// so unknown paths serve the SPA shell (pitfall P2).
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
			writeAPINotFound(w)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Try serving the exact file first.
		if _, err := fs.Stat(sub, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for client-side routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}
