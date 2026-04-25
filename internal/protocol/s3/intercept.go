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
	"io"
	"net/http"
	"os"
	"strings"

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

			h := sha256.New()
			tee := io.TeeReader(r.Body, h)
			_, copyErr := io.Copy(tmp, tee)
			closeErr := tmp.Close()
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
			defer reopened.Close()
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
