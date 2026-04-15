package httpx

import "net/http"

// MaintenanceMode returns a pass-through middleware in Phase 1. The real
// toggle (D-27; Phase 5) will read a settings row and return 503 for
// write-method routes when enabled. Exported now so the middleware chain is
// stable and downstream plans don't have to re-wire the router constructor.
func MaintenanceMode() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	}
}
