package httpx

import (
	"net/http"

	"github.com/dxc-internal/omnirepo/internal/httperr"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// MaintenanceMode returns middleware that blocks write-method requests when the
// settings table has maintenance_mode="true". GET, HEAD, OPTIONS always pass
// through (OPS-05: reads allowed during maintenance).
//
// When settings is nil (test mode, Phase 1 backward compat) the middleware
// passes through unconditionally.
func MaintenanceMode(settings *metadata.SettingsRepo) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reads always allowed.
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			// Nil-safe for tests that don't wire settings.
			if settings == nil {
				next.ServeHTTP(w, r)
				return
			}
			val, err := settings.Get(r.Context(), "maintenance_mode")
			if err == nil && val == "true" {
				// Phase 6 / plan 04: emit the canonical envelope so the
				// UI can branch on class=operator_action_required and
				// deep-link to /admin/maintenance for the operator who
				// can un-gate the request.
				w.Header().Set("Retry-After", "300")
				httperr.Write(w, r, httperr.OperatorRequired(
					"maintenance.enabled",
					"Write operations are disabled during maintenance.",
					"/admin/maintenance",
					"Go to Admin → Maintenance",
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
