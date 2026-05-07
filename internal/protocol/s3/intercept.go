package s3

// Plan 02-03 / S3HARD-03: chi-side PutObject SHA enforcement.
//
// gofakes3 hijacks r.Body at vendor/.../gofakes3.go:686-701 by wrapping the
// inbound reader with its own *hashingReader (and *chunkedReader for the
// STREAMING sentinel) BEFORE invoking Backend.PutObject(input io.Reader).
// A wrapper-on-r.Body pattern (the original 02-03 design) cannot survive
// the gofakes3 boundary — the Backend method only ever sees *hashingReader.
//
// So we move the SHA enforcement OUTSIDE gofakes3: a chi middleware at the
// route boundary intercepts single-shot PUT requests, validates the body,
// and only forwards to gofakes3 (or rejects). On mismatch the request never
// reaches gofakes3, so backend.PutObject + storage.WriteAndRename + the DB
// INSERT never run, and the destination key at <bucketRoot>/<key> is never
// touched. Pre-existing objects therefore survive a rejected PUT byte-for-
// byte (B-2 destructive-overwrite fix proven by Test 2 in intercept_test.go).
//
// Multipart-related PUTs (`?uploads`, `?uploadId`, `?partNumber`) pass
// through unchanged — multipart integrity is handled by gofakes3 +
// UploadPart / CompleteMultipartUpload paths (Plan 02-04 owns the
// CreateMultipartUpload SHA enforcement via interceptCreateMultipartUpload
// in this same file).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	"github.com/dxc-internal/omnirepo/internal/protocol/s3/sigv4"
)

// SigV4 sentinel literals for the x-amz-content-sha256 header. Keeping them
// inline (one site) avoids cross-package imports and keeps the bypass logic
// readable.
const (
	sentinelUnsignedPayload = "UNSIGNED-PAYLOAD"
	sentinelStreaming       = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
)

// interceptPutObject enforces the SigV4-declared x-amz-content-sha256 for
// single-shot PUT requests at the chi layer.
//
// Bypass paths (in order, short-circuiting):
//  1. Non-PUT request                                  → forward
//  2. Multipart-related PUT (`?uploads`, `?uploadId`,
//     `?partNumber`)                                   → forward
//  3. Missing PayloadSHAFromContext (programming error,
//     SigV4Middleware did not run upstream)            → fail closed (400)
//  4. UNSIGNED-PAYLOAD                                 → forward
//  5. STREAMING-AWS4-HMAC-SHA256-PAYLOAD              → forward (chunk
//     verifier inside gofakes3 enforces per-chunk integrity)
//
// Hex-mode enforcement path:
//  - Stage r.Body to a temp file in the bucket-shared tmpRoot, computing
//    sha256 inline via io.TeeReader.
//  - On mismatch: os.Remove(tmp), return AWS-shape XAmzContentSHA256Mismatch
//    HTTP 400 via sigv4.WriteError (D-05). next.ServeHTTP is NEVER called.
//  - On match: re-open the temp file, replace r.Body with the re-issued
//    reader, forward to next. The temp file is removed by the deferred
//    os.Remove once next returns.
//
// Threat coverage (T-02-03-01..06 — see Plan 02-03 <threat_model>):
//  - T-02-03-01 (signed SHA-A but sent SHA-B): mitigated by the compare.
//  - T-02-03-02 (missing PayloadSHA): mitigated by step 3 fail-closed.
//  - T-02-03-04 (DoS via large rejected body): documented accepted cost
//    — the body must be fully read to compute the streamed sha; cleanup
//    via deferred os.Remove.
//  - T-02-03-06 (multipart accidental enforcement): mitigated by step 2.
func interceptPutObject(b *backend.Backend) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: non-PUT bypass.
			if r.Method != http.MethodPut {
				next.ServeHTTP(w, r)
				return
			}

			// Step 2: multipart-related PUT bypass. The Has-check on raw
			// query keys covers `?uploads` (presence), `?uploadId=...`,
			// and `?partNumber=...` — single-shot PUT carries none of these.
			q := r.URL.Query()
			if q.Has("uploads") || q.Has("uploadId") || q.Has("partNumber") {
				next.ServeHTTP(w, r)
				return
			}

			// Step 3: read expected SHA stashed by SigV4Middleware (Plan 02-01).
			expected, ok := PayloadSHAFromContext(r.Context())
			if !ok {
				// SigV4Middleware should have run upstream. Fail closed —
				// silent bypass would mask a config bug and reopen the
				// audit-finding-#2 hole this plan exists to close.
				sigv4.WriteError(w, r, sigv4.ErrContentSHA256Mismatch)
				return
			}

			// Steps 4-5: sentinel bypass. UNSIGNED-PAYLOAD is the AWS-spec
			// opt-out (client took responsibility). STREAMING uses the
			// chunked-encoding verifier inside gofakes3.
			switch expected {
			case sentinelUnsignedPayload, sentinelStreaming:
				next.ServeHTTP(w, r)
				return
			}

			// Hex-mode enforcement: stage body to a temp file under tmpRoot.
			tmpDir := b.TmpRoot()
			if err := os.MkdirAll(tmpDir, 0o750); err != nil {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}
			tmp, err := os.CreateTemp(tmpDir, "putobject-sha-*")
			if err != nil {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}
			tmpPath := tmp.Name()
			// Best-effort cleanup on every exit path. The deferred remove
			// runs after either branch — for the match path it removes the
			// staged file once gofakes3 has consumed the re-issued reader.
			defer func() { _ = os.Remove(tmpPath) }()

			// Codex P2-02: capture original r.Body so we can Close it
			// explicitly before swapping. Net/http would Close it itself
			// at end of request, but gofakes3 (the next handler) may
			// inspect or replace r.Body in ways that would skip the
			// original Close. Belt-and-suspenders.
			origBody := r.Body
			h := sha256.New()
			tee := io.TeeReader(origBody, h)
			_, copyErr := io.Copy(tmp, tee)
			closeErr := tmp.Close()
			_ = origBody.Close()
			if copyErr != nil {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}
			if closeErr != nil {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}

			computed := hex.EncodeToString(h.Sum(nil))
			if !strings.EqualFold(expected, computed) {
				// MISMATCH: gofakes3 is bypassed entirely. backend.PutObject
				// + storage.WriteAndRename + the DB INSERT never run, so the
				// dst file at <bucketRoot>/<key> is not touched. Pre-existing
				// objects at the same key survive byte-for-byte (T2 / B-2).
				sigv4.WriteError(w, r, sigv4.ErrContentSHA256Mismatch)
				return
			}

			// MATCH: re-open the temp file as a Reader and replace r.Body
			// so gofakes3 sees the same bytes the verifier just hashed.
			reopened, err := os.Open(tmpPath)
			if err != nil {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}
			defer func() { _ = reopened.Close() }()
			r.Body = io.NopCloser(reopened)
			next.ServeHTTP(w, r)
		})
	}
}

// InterceptPutObjectForTest exposes interceptPutObject for the s3_test
// package's intercept_test.go (Plan 02-03 Task 2). Internal callers should
// use interceptPutObject directly — handler.go does so when mounting.
func InterceptPutObjectForTest(b *backend.Backend) func(http.Handler) http.Handler {
	return interceptPutObject(b)
}

// interceptCreateMultipartUpload owns the `?uploads` POST route at the chi
// layer (Plan 02-04, S3HARD-05 / S3HARD-06, audit finding #10).
//
// The motivation: gofakes3's MultipartBackend.CreateMultipartUpload signature
// drops the *http.Request argument, so the Backend method has no way to read
// the SigV4-resolved Actor.S3KeyID off r.Context(). To stamp
// s3_multipart_uploads.initiated_by_s3_key_id correctly (replacing the
// legacy users.id=1 fabrication that closed audit-finding-#10) we hijack
// the route at the chi layer — read actor.S3KeyID from ctx, dispatch to the
// new actor-aware backend.CreateMultipartUploadCtx, and render the AWS-spec
// `<InitiateMultipartUploadResult>` XML envelope ourselves. gofakes3 is
// bypassed entirely for this single route.
//
// All other multipart routes (UploadPart `?partNumber=`, CompleteMultipart
// `POST ?uploadId=`, AbortMultipartUpload `DELETE ?uploadId=`,
// ListMultipartUploads `GET ?uploads`, ListParts `GET ?uploadId=`) continue
// to flow through gofakes3 unchanged — they look up rows by uploadId, which
// now has the correct initiated_by_s3_key_id.
//
// Bypass paths (in order, short-circuiting):
//  1. Non-POST request                                          → forward
//  2. POST without `?uploads` (other multipart subroutes)       → forward
//  3. Missing actor.S3KeyID on ctx (programming error: SigV4
//     middleware did not run upstream)                          → fail closed
//  4. Bucket/key cannot be parsed from URL                       → 400
//
// Hex-mode enforcement is not relevant here — the body of an
// InitiateMultipartUpload POST is empty per AWS spec; metadata travels via
// x-amz-meta-* headers which we forward to the backend opaquely.
//
// Threat coverage (T-02-04-01..06 — see Plan 02-04 <threat_model>):
//   - T-02-04-01 (spoofing) mitigated by reading actor.S3KeyID off ctx
//     (only SigV4Middleware sets it, after a verified signature).
//   - T-02-04-02 (unauthenticated multipart-create) mitigated by route
//     placement under SigV4Middleware + RequireBucketAccess.
//   - T-02-04-06 (x-amz-meta passthrough) — same trust posture as gofakes3
//     would have applied; metadata is stored opaquely as JSON.
func interceptCreateMultipartUpload(b *backend.Backend) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: non-POST bypass.
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			// Step 2: only `?uploads` (presence) is the create route. Other
			// `?uploadId=...` POSTs are CompleteMultipartUpload (gofakes3 owns).
			q := r.URL.Query()
			if !q.Has("uploads") {
				next.ServeHTTP(w, r)
				return
			}

			// Step 3: actor.S3KeyID must be on ctx. Fail closed on programming
			// error — silent bypass would leave the row attribution path
			// unenforced. SigV4Middleware always stamps it on success.
			actor, ok := auth.ActorFromContext(r.Context())
			if !ok || actor.S3KeyID == nil {
				sigv4.WriteError(w, r, sigv4.ErrInvalidAccessKeyId)
				return
			}

			// Step 4: parse bucket + key from path. Path arrives as
			// "/<bucket>/<key>" (chi.StripPrefix removed /s3 upstream) OR
			// "/s3/<bucket>/<key>" (depending on whether the test mounts the
			// strip-prefix or hits the chi route directly). Handle both.
			bucket := bucketFromPath(r.URL.Path)
			if bucket == "" {
				// Try without /s3 prefix (path already stripped).
				bucket = bucketFromStrippedPath(r.URL.Path)
			}
			if bucket == "" {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}
			key := multipartKeyFromPath(r.URL.Path, bucket)
			if key == "" {
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}

			// Forward x-amz-meta-* headers + Content-Type to the backend as
			// opaque metadata. gofakes3 normalizes to map[string]string —
			// we mirror its passthrough shape.
			meta := metaFromMultipartHeaders(r)

			uploadID, err := b.CreateMultipartUploadCtx(r.Context(), bucket, key, meta, actor.S3KeyID)
			if err != nil {
				// Map to a generic AWS-shape error envelope. Detailed cause
				// stays in slog at Warn — never leaked over the wire.
				sigv4.WriteError(w, r, sigv4.ErrInvalidRequest)
				return
			}

			// Render AWS-spec InitiateMultipartUploadResult.
			resp := struct {
				XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
				XMLNS    string   `xml:"xmlns,attr"`
				Bucket   string   `xml:"Bucket"`
				Key      string   `xml:"Key"`
				UploadID string   `xml:"UploadId"`
			}{
				XMLNS:    "http://s3.amazonaws.com/doc/2006-03-01/",
				Bucket:   bucket,
				Key:      key,
				UploadID: string(uploadID),
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(xml.Header))
			_ = xml.NewEncoder(w).Encode(resp)
		})
	}
}

// InterceptCreateMultipartUploadForTest exposes interceptCreateMultipartUpload
// for the s3_test package's intercept_multipart_test.go. Internal callers
// should use interceptCreateMultipartUpload directly — handler.go does so
// when mounting (Plan 02-04 Task 1).
func InterceptCreateMultipartUploadForTest(b *backend.Backend) func(http.Handler) http.Handler {
	return interceptCreateMultipartUpload(b)
}

// bucketFromStrippedPath extracts the bucket name from a path that has
// already had any /s3 prefix removed (e.g. "/<bucket>/<key>"). Returns ""
// for paths without a leading slash + bucket segment.
func bucketFromStrippedPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	rest := path[1:]
	if rest == "" {
		return ""
	}
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		return rest[:idx]
	}
	return rest
}

// multipartKeyFromPath returns the object key from a chi-routed path that
// looks like "/s3/<bucket>/<key>" or "/<bucket>/<key>" once the /s3 prefix
// has been stripped upstream. The key may itself contain '/' (e.g. a/b/c).
//
// Returns "" if the path does not contain a key segment after the bucket.
func multipartKeyFromPath(path, bucket string) string {
	// Try /s3/<bucket>/<key> first.
	if rest, ok := strings.CutPrefix(path, "/s3/"+bucket+"/"); ok {
		return rest
	}
	// Then /<bucket>/<key>.
	if rest, ok := strings.CutPrefix(path, "/"+bucket+"/"); ok {
		return rest
	}
	return ""
}

// metaFromMultipartHeaders builds the meta map gofakes3 would have populated
// from x-amz-meta-* + Content-Type + x-amz-acl headers. Caller-supplied
// metadata is stored opaquely as JSON in s3_multipart_uploads.metadata_json
// — no downstream code interprets it (same trust posture as gofakes3's
// native handling — T-02-04-06).
func metaFromMultipartHeaders(r *http.Request) map[string]string {
	if len(r.Header) == 0 {
		return nil
	}
	m := make(map[string]string, len(r.Header))
	for k, vs := range r.Header {
		if len(vs) == 0 {
			continue
		}
		m[k] = vs[0]
	}
	return m
}
