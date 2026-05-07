// Package api — OCI action endpoints (Phase 02-10, D-04, D-05).
//
// Mounts two REST endpoints under the already-auth'd /api/v1 subtree:
//
//	POST /api/v1/projects/{name}/repos/docker/{repo}/pull-external
//	POST /api/v1/projects/{name}/repos/docker/{repo}/promote
//
// Both handlers live on the OCI Handler (internal/protocol/oci) so they
// reuse its project/repo resolution, auth helpers, audit emitter, and
// shared writeManifestWithRefcounts helper. This file is a thin wiring
// shim: when ScansDeps-style deps are nil, the routes are skipped so
// tests that boot the admin REST without the OCI surface continue to work.
package api

import (
	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
)

// OCIActionsDeps bundles the REST handlers for /api/v1 OCI actions.
type OCIActionsDeps struct {
	PullExternal *oci.PullExternalREST
	Promote      *oci.PromoteREST
	DeleteTag    *oci.DeleteTagREST
}

// RegisterOCIActionsRoutes mounts the handlers on r. No-op when d is nil.
// The caller (internal/app.Run) constructs d with all handlers wired.
func RegisterOCIActionsRoutes(r chi.Router, d *OCIActionsDeps) {
	if d == nil {
		return
	}
	if d.PullExternal != nil {
		r.Post("/projects/{name}/repos/docker/{repo}/pull-external", d.PullExternal.Handle)
	}
	if d.Promote != nil {
		r.Post("/projects/{name}/repos/docker/{repo}/promote", d.Promote.Handle)
	}
	if d.DeleteTag != nil {
		r.Delete("/projects/{name}/repos/docker/{repo}/tags/{tag}", d.DeleteTag.Handle)
	}
}
