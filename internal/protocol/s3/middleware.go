package s3

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vladoportos/omnirepo/internal/auth"
	s3keys "github.com/vladoportos/omnirepo/internal/protocol/s3/keys"
	"github.com/vladoportos/omnirepo/internal/protocol/s3/sigv4"
)

// payloadSHAKey carries the SigV4-verified declared payload-SHA256 (the
// literal x-amz-content-sha256 value: 64-hex / "UNSIGNED-PAYLOAD" /
// "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"). PutObject reads this to enforce
// the post-write SHA compare; multipart paths ignore it.
type payloadSHAKey struct{}

// WithPayloadSHA returns ctx annotated with the verifier-attested payload
// SHA. SigV4Middleware is the sole production caller; tests may also seed
// one to assert downstream behavior without standing up the full verify
// pipeline.
func WithPayloadSHA(ctx context.Context, sha string) context.Context {
	return context.WithValue(ctx, payloadSHAKey{}, sha)
}

// PayloadSHAFromContext returns the declared payload SHA stashed by
// SigV4Middleware. Returns ("", false) when absent (non-S3 routes that
// never ran the middleware).
func PayloadSHAFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(payloadSHAKey{}).(string)
	return v, ok
}

// actorCtxKey stashes the auth.Actor resolved by SigV4Middleware into the
// request context. Downstream middleware (RequireBucketAccess) reads it.
type actorCtxKey struct{}

// RejectNonSigV4 is chi middleware that short-circuits any request whose
// Authorization header is NOT an AWS SigV4 signature: session cookies and
// Bearer tokens are never accepted on /s3/*.
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
// On failure it writes the appropriate AWS-shape XML error and
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

			// Use a capturing lookup that stores the full result (secret +
			// project_id) from one DB query, avoiding the double-lookup of
			// Lookup + ResolveProject.
			var lookupResult *s3keys.LookupResult
			capturingLookup := func(akid string) (string, error) {
				r, err := service.LookupFull(akid)
				if err != nil {
					return "", err
				}
				lookupResult = r
				return r.Secret, nil
			}

			result, err := sigv4.Verify(verifyReq, capturingLookup, skew)
			if err != nil {
				sigv4.WriteError(w, r, err)
				return
			}

			var projectID int64
			if lookupResult != nil {
				projectID = lookupResult.ProjectID
			} else {
				pid, perr := service.ResolveProject(result.AccessKeyID)
				if perr != nil {
					sigv4.WriteError(w, r, perr)
					return
				}
				projectID = pid
			}

			actor := auth.Actor{
				Kind:         auth.ActorKindS3Key,
				ProjectScope: &projectID,
			}
			// Populate S3KeyID from the resolved s3_access_keys row. The
			// capturingLookup path always runs ahead of the ResolveProject
			// fallback, so lookupResult is non-nil on every successful
			// SigV4 verify in production. The fallback path is dead code
			// today (kept for defense-in-depth) — leave S3KeyID nil there.
			if lookupResult != nil {
				id := lookupResult.ID
				actor.S3KeyID = &id
			}
			ctx := context.WithValue(r.Context(), actorCtxKey{}, actor)
			ctx = auth.WithActor(ctx, actor)
			// Stash the declared payload SHA unconditionally — even for
			// UNSIGNED-PAYLOAD and STREAMING sentinels. PutObject decides
			// what to do based on the literal value.
			ctx = WithPayloadSHA(ctx, result.PayloadSHA256)

			// STREAMING (aws-chunked) uploads: sigv4.Verify swapped in a reader
			// that de-frames AND verifies the per-chunk signature chain, but it
			// landed on verifyReq.Body — which is r for path-style requests but
			// a throwaway clone for vhost requests. Move it onto the request we
			// actually forward, and rewrite x-amz-content-sha256 so gofakes3
			// does NOT wrap r.Body in a SECOND chunked reader (it keys that wrap
			// solely on this header — gofakes3.go createObject). Without this,
			// path-style uploads were double-de-framed (corruption) and
			// vhost uploads skipped chunk-signature verification entirely (the
			// verified clone was discarded). gofakes3 then reads object size
			// from Content-Length on the non-streaming path; for aws-chunked
			// that header is the ENCODED length, so swap in the decoded payload
			// length the client declared (absent/invalid → leave it, so a
			// non-compliant client fails cleanly instead of corrupting).
			if result.BodyMode == sigv4.BodyModeStreamingSigned {
				r.Body = verifyReq.Body
				r.Header.Set("X-Amz-Content-Sha256", sentinelUnsignedPayload)
				if dec := r.Header.Get("X-Amz-Decoded-Content-Length"); dec != "" {
					if n, perr := strconv.ParseInt(dec, 10, 64); perr == nil && n >= 0 {
						r.Header.Set("Content-Length", dec)
						r.ContentLength = n
					}
				}
			}
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
				// Root-level request (ListBuckets). S3 keys are
				// project-scoped so listing all buckets across projects
				// is a cross-project information disclosure. Block it.
				writeAccessDenied(w, r)
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
