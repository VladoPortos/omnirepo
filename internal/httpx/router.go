// Package httpx assembles the chi router, global middleware chain, and the
// reserved-prefix guard. Downstream plans mount their protocol sub-routers
// through MountReserved (which rejects reserved names) or through direct
// chi.Mount calls inside this package for system routes (healthz/readyz/api).
package httpx

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// Deps is the dependency bundle the router needs at construction time.
// Phase 1 only plumbs Config; later plans add Auth, DB, CertHolder, Audit.
type Deps struct {
	Config   config.Config
	Settings *metadata.SettingsRepo
	// LoginBoxSeeder seeds a request-scoped login box so downstream auth
	// middlewares can populate the authenticated login for the access log.
	// cmd/omnirepo passes an adapter over auth.WithLoginBox. Leaving it nil
	// keeps actor_id blank (early plans / tests without auth mounted).
	LoginBoxSeeder LoginBoxSeeder
}

// New constructs the OmniRepo chi router with the D-27 global middleware chain.
// Downstream plans mount protocol routers on the returned router through
// MountReserved (for project paths) or direct chi.Mount (for reserved paths).
//
// Phase 6 / ERR-07: chi's default request-id generator is replaced with
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
// internal/api/dev_error_routes.go (Phase 6 / plan 03 / T-06-03-04).
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

// NewBareRouter returns a chi router without any middleware. Intended for
// tests and MountReserved call-sites that want to test routing semantics
// without the global middleware chain side-effects.
func NewBareRouter() chi.Router {
	return chi.NewRouter()
}

// MountReserved mounts the given handler at /<prefix>, panicking if prefix is
// one of the D-26 reserved names (those may only be mounted by system code
// inside this package).
func MountReserved(r chi.Router, prefix string, h http.Handler) {
	if IsReserved(prefix) {
		panic(fmt.Sprintf("httpx: cannot mount at reserved prefix %q (IsReserved(%q)=true)", prefix, prefix))
	}
	if r == nil {
		// Tests call MountReserved with a nil router to exercise the panic
		// path; once the panic check passes we simply return — a real
		// mount would be the caller's responsibility.
		return
	}
	r.Mount("/"+prefix, h)
}
