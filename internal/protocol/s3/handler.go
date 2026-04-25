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
		// Plan 02-03 / S3HARD-03: chi-side PutObject SHA enforcement.
		// Mounted AFTER SigV4Middleware (which stashes the declared SHA in
		// ctx) and AFTER RequireBucketAccess, BEFORE the gofakes3 server
		// (which hijacks r.Body and would defeat any wrapper-on-r.Body
		// pattern). Mismatch returns 400 XAmzContentSHA256Mismatch and
		// gofakes3 is never reached — pre-existing dst objects survive
		// byte-for-byte (B-2 fix).
		r.Use(interceptPutObject(d.Backend))
		// Plan 02-04 / S3HARD-05 / S3HARD-06 (audit finding #10): chi-side
		// CreateMultipartUpload attribution. Hijacks `?uploads` POST so we
		// can read actor.S3KeyID off ctx (gofakes3 drops *http.Request from
		// MultipartBackend.CreateMultipartUpload's signature) and persist
		// s3_multipart_uploads.initiated_by_s3_key_id correctly. All other
		// multipart subroutes (UploadPart / Complete / Abort / List)
		// continue through gofakes3 unchanged — they look up rows by
		// uploadId, which now has the correct attribution.
		r.Use(interceptCreateMultipartUpload(d.Backend))
		r.Handle("/*", http.StripPrefix("/s3", server))
	})
}
