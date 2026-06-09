package s3

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/johannesboyne/gofakes3"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/protocol/s3/backend"
	s3keys "github.com/vladoportos/omnirepo/internal/protocol/s3/keys"
)

// Deps holds the dependencies for the S3 HTTP surface. Constructed in
// app.go and passed to Mount.
type Deps struct {
	// Service is the SigV4 AKID→secret bridge.
	Service *s3keys.Service

	// Backend is the gofakes3.Backend + MultipartBackend.
	Backend *backend.Backend

	// Skew is the maximum allowed clock skew for SigV4 verification.
	Skew time.Duration

	// Hostnames is cfg.Server.ExternalHostnames for v-host rewrite.
	Hostnames []string

	// Audit records object/bucket mutation events. nil disables protocol
	// auditing (tests).
	Audit audit.Logger
}

// Mount registers the S3 HTTP surface on parent. The middleware chain is:
//
//  1. VHostRewrite — must be registered BEFORE Mount via
//     parent.Use(VHostRewrite(hostnames)) in app.go (before any routes)
//  2. RejectNonSigV4 — blocks session/cookie auth
//  3. SigV4Middleware — verifies signature, stashes actor
//  4. RequireBucketAccess — checks auth.Can for bucket's project
//  5. gofakes3.Server — handles the actual S3 protocol
//
// IMPORTANT: Callers must register VHostRewrite as global middleware on
// the parent router BEFORE calling Mount and before any other routes.
// Chi requires all Use() calls before Route() calls.
func (d *Deps) Mount(parent chi.Router) {
	// Disable gofakes3's own time-skew check; our SigV4Middleware already
	// verifies clock skew with proper XML error responses. Route gofakes3's
	// internal logging (backend DB/IO failures that surface as 500
	// InternalError XML) onto slog so S3 errors are operationally visible
	// like every other protocol's.
	server := gofakes3.New(d.Backend,
		gofakes3.WithTimeSkewLimit(0),
		gofakes3.WithLogger(slogAdapter{}),
	).Server()

	parent.Route("/s3", func(r chi.Router) {
		r.Use(RejectNonSigV4)
		r.Use(SigV4Middleware(d.Service, d.Skew))
		// Audit sits after SigV4 (actor on ctx) and before bucket-access
		// enforcement so 403 denials are recorded with outcome=denied.
		r.Use(AuditMiddleware(d.Audit))
		r.Use(RequireBucketAccess(d.Backend.FindBucketProjectID))
		// chi-side PutObject SHA enforcement.
		// Mounted AFTER SigV4Middleware (which stashes the declared SHA in
		// ctx) and AFTER RequireBucketAccess, BEFORE the gofakes3 server
		// (which hijacks r.Body and would defeat any wrapper-on-r.Body
		// pattern). Mismatch returns 400 XAmzContentSHA256Mismatch and
		// gofakes3 is never reached — pre-existing dst objects survive
		// byte-for-byte.
		r.Use(interceptPutObject(d.Backend))
		// chi-side CreateMultipartUpload attribution. Hijacks `?uploads` POST so we
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
