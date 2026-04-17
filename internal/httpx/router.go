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
//
// Phase 6 / plan 03: MountDevErrorRoutes is an optional hook used by
// app.Run to register api.MountDevErrorRoutes onto the router when
// OMNIREPO_DEV=1. The hook pattern avoids a direct internal/httpx →
// internal/api import cycle (api already imports httpx in
// sync_actions.go). When nil, New() is a no-op for this hook — the
// production default.
type Deps struct {
	Config              config.Config
	Settings            *metadata.SettingsRepo
	MountDevErrorRoutes func(chi.Router)
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
	r.Use(StructuredLogger(d.Config))
	r.Use(AuditEnter)
	r.Use(MaintenanceMode(d.Settings))
	r.Use(AuditExit)
	// Phase 6 / plan 03: opt-in dev-only canned error routes. app.Run
	// passes api.MountDevErrorRoutes in here; the function itself is a
	// no-op unless OMNIREPO_DEV=1 at process start. Kept behind a hook
	// on Deps so internal/httpx does not import internal/api (api →
	// httpx already exists, so the reverse edge would cycle).
	if d.MountDevErrorRoutes != nil {
		d.MountDevErrorRoutes(r)
	}
	return r
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
