package s3

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/johannesboyne/gofakes3"

	"github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	s3keys "github.com/dxc-internal/omnirepo/internal/protocol/s3/keys"
)

// Deps holds the dependencies for the S3 HTTP surface. Constructed in
// app.go and passed to Mount.
type Deps struct {
	// Service is the SigV4 AKID→secret bridge (Plan 05).
	Service *s3keys.Service

	// Backend is the gofakes3.Backend + MultipartBackend (Plan 06).
	Backend *backend.Backend

	// Skew is the maximum allowed clock skew for SigV4 verification.
	Skew time.Duration

	// Hostnames is cfg.Server.ExternalHostnames for v-host rewrite.
	Hostnames []string
}

// Mount registers the S3 HTTP surface on parent. The middleware chain is:
//
//  1. VHostRewrite — must be registered BEFORE Mount via
//     parent.Use(VHostRewrite(hostnames)) in app.go (before any routes)
//  2. RejectNonSigV4 — blocks session/cookie auth (D-08)
//  3. SigV4Middleware — verifies signature, stashes actor
//  4. RequireBucketAccess — checks auth.Can for bucket's project
//  5. gofakes3.Server — handles the actual S3 protocol
//
// This matches D-16 mount order exactly.
//
// IMPORTANT: Callers must register VHostRewrite as global middleware on
// the parent router BEFORE calling Mount and before any other routes.
// Chi requires all Use() calls before Route() calls.
func (d *Deps) Mount(parent chi.Router) {
	// Disable gofakes3's own time-skew check; our SigV4Middleware already
	// verifies clock skew with proper XML error responses.
	server := gofakes3.New(d.Backend, gofakes3.WithTimeSkewLimit(0)).Server()

	parent.Route("/s3", func(r chi.Router) {
		r.Use(RejectNonSigV4)
		r.Use(SigV4Middleware(d.Service, d.Skew))
		r.Use(RequireBucketAccess(d.Backend.FindBucketProjectID))
		r.Handle("/*", http.StripPrefix("/s3", server))
	})
}
