// Package httpx assembles the chi router, global middleware chain, and the
// reserved-prefix guard (IsReserved). Protocol sub-routers use route
// patterns like /{project}/git/{repo}; reserved system prefixes
// (healthz/readyz/api/v2/...) are mounted directly and project names are
// validated against ReservedPrefixes at creation time.
package httpx

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// Deps is the dependency bundle the router needs at construction time.
type Deps struct {
	Config   config.Config
	Settings *metadata.SettingsRepo
	// LoginBoxSeeder seeds a request-scoped login box so downstream auth
	// middlewares can populate the authenticated login for the access log.
	// cmd/omnirepo passes an adapter over auth.WithLoginBox. Leaving it nil
	// keeps actor_id blank (tests without auth mounted).
	LoginBoxSeeder LoginBoxSeeder
}

// New constructs the OmniRepo chi router with the global middleware chain.
//
// chi's default request-id generator is replaced with
// IncidentIDMiddleware (UUID v7) and chi's default panic recoverer with
// EnvelopeRecoverer (httperr.Internal envelope on panic). Ordering matters —
// IncidentIDMiddleware must run first so EnvelopeRecoverer's slog record
// and envelope body carry the same incident_id.
func New(d Deps) chi.Router {
	r := chi.NewRouter()
	r.Use(IncidentIDMiddleware)
	r.Use(middleware.RealIP)
	r.Use(EnvelopeRecoverer)
	r.Use(StructuredLogger(d.Config, d.LoginBoxSeeder))
	r.Use(AuditEnter)
	r.Use(MaintenanceMode(d.Settings))
	r.Use(AuditExit)
	return r
}

// MountDevErrorRoutes is a type alias for the hook signature that
// app.Run uses to register api.MountDevErrorRoutes onto the router AFTER
// all middleware has been installed (chi panics when a Use call follows
// a route registration). Keeping the reference here documents the
// integration point and gives callers a single place to look up the
// expected signature. The concrete implementation lives in
// internal/api/dev_error_routes.go.
//
// Usage pattern from app.Run:
//
//	router := httpx.New(httpx.Deps{...})
//	router.Use(otherMiddleware) // must complete before first route
//	httpx.MountDevErrorRoutes(router, api.MountDevErrorRoutes)
type MountDevErrorRoutesFn = func(chi.Router)

// MountDevErrorRoutes invokes fn against r only if fn is non-nil.
// Exists so app.Run can wire api.MountDevErrorRoutes through a named
// helper without creating an internal/httpx → internal/api import
// cycle (api → httpx already exists in sync_actions.go).
func MountDevErrorRoutes(r chi.Router, fn MountDevErrorRoutesFn) {
	if fn == nil {
		return
	}
	fn(r)
}
