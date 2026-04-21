package httpx

import (
	"net/http"

	"github.com/dxc-internal/omnirepo/internal/httperr"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// maintenanceToggleRoute is the one write endpoint that must bypass the
// maintenance gate — otherwise enabling maintenance mode permanently bricks
// the instance (the toggle itself is a POST). Admin auth is still enforced
// downstream by authmw.RequireCan(ActionTriggerGC) on the handler.
const maintenanceToggleRoute = "/api/v1/admin/maintenance"

// MaintenanceMode returns middleware that blocks write-method requests when the
// settings table has maintenance_mode="true". GET, HEAD, OPTIONS always pass
// through (OPS-05: reads allowed during maintenance). The maintenance-toggle
// POST endpoint itself is also allowed through so operators can disable
// maintenance from the UI.
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
			// Self-unbrick: allow the POST that toggles maintenance so the
			// operator can disable it once it's enabled. PUT/PATCH/DELETE
			// on the same path stay gated (chi 405s them anyway since the
			// handler only registers POST + GET, but be explicit).
			if r.Method == http.MethodPost && r.URL.Path == maintenanceToggleRoute {
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
