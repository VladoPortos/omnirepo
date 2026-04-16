package s3

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/protocol/s3/keys"
	"github.com/dxc-internal/omnirepo/internal/protocol/s3/sigv4"
)

// actorCtxKey stashes the auth.Actor resolved by SigV4Middleware into the
// request context. Downstream middleware (RequireBucketAccess) reads it.
type actorCtxKey struct{}

// RejectNonSigV4 is chi middleware that short-circuits any request whose
// Authorization header is NOT an AWS SigV4 signature. This enforces D-08:
// session cookies and Bearer tokens are never accepted on /s3/*.
//
// If the Authorization header is absent or does not start with
// "AWS4-HMAC-SHA256 ", the request is rejected with 403 InvalidAccessKeyId
// (same error as an unknown AKID — no oracle about auth scheme).
func RejectNonSigV4(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if authz == "" || !strings.HasPrefix(authz, "AWS4-HMAC-SHA256 ") {
			sigv4.WriteError(w, r, sigv4.ErrInvalidAccessKeyId)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SigV4Middleware returns chi middleware that verifies the AWS SigV4 signature
// on every request. On success, it stashes an auth.Actor (kind=ActorKindS3Key,
// ProjectScope set to the AKID's project) into the request context.
//
// On failure it writes the appropriate AWS-shape XML error (D-12) and
// short-circuits — gofakes3 never sees unauthenticated bytes.
func SigV4Middleware(service *s3keys.Service, skew time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If VHostRewrite changed the path, temporarily restore the
			// original for SigV4 verification — the client signed the
			// pre-rewrite path (e.g. "/<key>" not "/s3/<bucket>/<key>").
			origPath := OriginalPath(r)
			verifyReq := r
			if origPath != r.URL.Path {
				verifyReq = r.Clone(r.Context())
				verifyReq.URL.Path = origPath
			}

			result, err := sigv4.Verify(verifyReq, service.Lookup, skew)
			if err != nil {
				sigv4.WriteError(w, r, err)
				return
			}

			// Resolve the project that owns this AKID.
			projectID, perr := service.ResolveProject(result.AccessKeyID)
			if perr != nil {
				sigv4.WriteError(w, r, perr)
				return
			}

			actor := auth.Actor{
				Kind:         auth.ActorKindS3Key,
				ProjectScope: &projectID,
			}
			ctx := context.WithValue(r.Context(), actorCtxKey{}, actor)
			ctx = auth.WithActor(ctx, actor)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// BucketProjectLookup resolves a bucket name to its owning project_id.
// Returns (0, false, nil) when the bucket does not exist.
type BucketProjectLookup func(ctx context.Context, name string) (projectID int64, found bool, err error)

// RequireBucketAccess returns chi middleware that extracts the bucket name
// from the URL path, looks up its owning project, and calls auth.Can to
// verify the authenticated actor has the required permission.
//
// Action dispatch by HTTP method:
//
//	GET, HEAD         -> ActionS3BucketRead
//	PUT, POST, DELETE -> ActionS3BucketWrite
//
// The actor is read from the request context (set by SigV4Middleware).
func RequireBucketAccess(lookup BucketProjectLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bucket := bucketFromPath(r.URL.Path)
			if bucket == "" {
				// Root-level request (e.g. ListBuckets). Let gofakes3
				// handle it — the actor is already authenticated.
				next.ServeHTTP(w, r)
				return
			}

			projectID, found, err := lookup(r.Context(), bucket)
			if err != nil {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}
			if !found {
				// Let gofakes3 handle the NoSuchBucket error naturally.
				next.ServeHTTP(w, r)
				return
			}

			actor, ok := auth.ActorFromContext(r.Context())
			if !ok {
				sigv4.WriteError(w, r, sigv4.ErrInvalidAccessKeyId)
				return
			}

			action := actionFromMethod(r.Method)
			target := auth.Target{
				Kind:      "bucket",
				ProjectID: projectID,
			}

			allowed, _ := auth.Can(r.Context(), actor, action, target)
			if !allowed {
				writeAccessDenied(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// actionFromMethod maps an HTTP method to an S3 bucket action.
func actionFromMethod(method string) auth.Action {
	switch method {
	case http.MethodGet, http.MethodHead:
		return auth.ActionS3BucketRead
	default:
		return auth.ActionS3BucketWrite
	}
}

// bucketFromPath extracts the bucket name from a path like "/s3/<bucket>/..."
// or "/s3/<bucket>". Returns "" for root-level paths like "/s3" or "/s3/".
func bucketFromPath(path string) string {
	// Strip /s3/ prefix.
	trimmed := strings.TrimPrefix(path, "/s3/")
	if trimmed == "" || trimmed == path {
		return ""
	}
	// Bucket is everything up to the next slash.
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

// writeAccessDenied writes a 403 AccessDenied XML error body.
func writeAccessDenied(w http.ResponseWriter, r *http.Request) {
	// Use a custom AccessDenied error — not one of the sigv4 sentinels.
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<Error><Code>AccessDenied</Code>` +
		`<Message>Access Denied</Message></Error>`))
}
