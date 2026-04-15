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
)

// Deps is the dependency bundle the router needs at construction time.
// Phase 1 only plumbs Config; later plans add Auth, DB, CertHolder, Audit.
type Deps struct {
	Config config.Config
}

// New constructs the OmniRepo chi router with the D-27 global middleware chain.
// Downstream plans mount protocol routers on the returned router through
// MountReserved (for project paths) or direct chi.Mount (for reserved paths).
func New(d Deps) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(StructuredLogger(d.Config))
	r.Use(MaintenanceMode())
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
