// Package api — Phase 03 Plan 06 SYNC-05 REST mount shim.
//
// Wires httpx.MountSyncRoutes into the api authenticated subtree. Lives
// in this package so it can import both internal/auth (to read
// auth.Actor off ctx) and internal/httpx (to call MountSyncRoutes)
// without forcing the cycle httpx → auth.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/httpx"
)

// SyncRESTAdapter is the api-package wrapper around httpx.SyncRESTDeps.
// It pre-fills the ActorResolver field with a function that bridges the
// auth.Actor on ctx to the auth-agnostic httpx.SyncActor surface.
type SyncRESTAdapter struct {
	Deps httpx.SyncRESTDeps
}

// NewSyncRESTAdapter constructs an adapter, fixing the ActorResolver to
// the canonical auth-bridge implementation.
func NewSyncRESTAdapter(deps httpx.SyncRESTDeps) *SyncRESTAdapter {
	deps.ActorResolver = SyncActorBridge
	return &SyncRESTAdapter{Deps: deps}
}

// SyncActorBridge maps auth.ActorFromContext into httpx.SyncActor. Exposed
// so the app package can wire it directly when constructing a SyncRESTDeps
// outside of NewSyncRESTAdapter.
func SyncActorBridge(r *http.Request) httpx.SyncActor {
	a, ok := auth.ActorFromContext(r.Context())
	if !ok || a.Kind == auth.ActorKindAnonymous {
		return httpx.SyncActor{}
	}
	out := httpx.SyncActor{Authenticated: true, IsSuperAdmin: a.IsSuperAdmin}
	switch a.Kind {
	case auth.ActorKindUser:
		out.UserID = a.ID
	case auth.ActorKindAPIKey:
		out.APIKeyID = a.APIKeyID
		switch {
		case a.ProjectScope != nil:
			// Project-owned API key: pin every access to that project.
			out.ProjectID = *a.ProjectScope
		case a.OwnerKind == auth.OwnerKindUser:
			// User-owned API key: per the Actor contract, a.ID already holds
			// the owning user's id. Surface it here so the downstream
			// membership branch (`actor.UserID != 0` in handleSync) resolves
			// against the owning user's project memberships — the same
			// shape auth.ResolveMembership applies for every protocol
			// handler since F-05.1. Missing this line made user-owned API
			// keys 403 on POST .../sync even when the owning user was a
			// project member (F-06.5; 11th site of the F-05.1 family).
			out.UserID = a.ID
		}
	}
	return out
}

// RegisterSyncRoutes mounts the sync REST endpoint on r when adapter is
// non-nil. The handler is registered under the caller's existing /api/v1
// subtree, so the final URL is POST
// /api/v1/projects/{name}/repos/{type}/{repo}/sync.
func RegisterSyncRoutes(r chi.Router, adapter *SyncRESTAdapter) {
	if adapter == nil {
		return
	}
	httpx.MountSyncRoutes(r, adapter.Deps)
}
