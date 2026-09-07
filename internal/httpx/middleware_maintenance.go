package httpx

import (
	"net/http"
	"strings"

	"github.com/vladoportos/omnirepo/internal/httperr"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// maintenanceToggleRoute is the administrative write endpoint that bypasses the
// maintenance gate — otherwise enabling maintenance mode permanently bricks
// the instance (the toggle itself is a POST). Admin auth is still enforced
// downstream by authmw.RequireCan(ActionTriggerGC) on the handler.
const maintenanceToggleRoute = "/api/v1/admin/maintenance"

// MaintenanceMode returns middleware that blocks write-method requests when the
// settings table has maintenance_mode="true". GET, HEAD, OPTIONS always pass
// through (reads allowed during maintenance). Authentication, the maintenance
// toggle, and Git upload-pack POST reads also remain available.
//
// When settings is nil (test mode, backward compat) the middleware
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
			// Preserve administrative recovery and protocol reads.
			if maintenanceRecoveryOrRead(r) {
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
				// Emit the canonical envelope so the
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

// Authentication remains available so an administrator can recover an
// instance after their session expires. Git upload-pack is a read RPC even
// though the wire protocol uses POST. Match only the mounted route shapes.
func maintenanceRecoveryOrRead(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case maintenanceToggleRoute, "/api/v1/auth/login", "/api/v1/auth/logout", "/api/v1/auth/change-password":
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return len(parts) == 4 && parts[3] == "git-upload-pack" &&
		(parts[0] == "git" || parts[1] == "git") && parts[0] != "" && parts[1] != "" && parts[2] != ""
}
