// Package httpx — dev mode reverse proxy to Vite (UI-03, D-33).
//
// When OMNIREPO_DEV=1, the router uses DevProxy() as the NotFound handler
// instead of SPAHandler. This forwards non-API requests to the Vite dev
// server on :5173 for HMR and fast refresh during UI development.
package httpx

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

// DevProxy returns a reverse proxy handler that forwards requests to the
// Vite dev server at http://localhost:5173. /api/* and /v2/* NotFounds
// still return 404 JSON — otherwise unknown API routes get a Vite 404 HTML
// page, which masks legitimate routing bugs during development.
func DevProxy() http.Handler {
	target, _ := url.Parse("http://localhost:5173")
	proxy := httputil.NewSingleHostReverseProxy(target)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPILikePath(r.URL.Path) {
			writeAPINotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// IsDevMode returns true when the OMNIREPO_DEV environment variable is
// set to "1". When true, the router should use DevProxy() instead of
// SPAHandler for the NotFound handler.
func IsDevMode() bool {
	return os.Getenv("OMNIREPO_DEV") == "1"
}
