package s3_test

// interceptPutObject middleware tests.
//
// Coverage matrix:
//
//   T1 HexMismatchRejects               — 400 + AWS envelope; next NEVER called; no file at dst
//   T2 PreExistingObjectSurvives        — pre-staged "v1" at dst stays "v1" byte-for-byte after rejection
//   T3 HexMatchPassesThrough            — next called once; body reads back original bytes
//   T4 UnsignedPayloadBypass            — UNSIGNED-PAYLOAD; pass-through verbatim
//   T5 StreamingSentinelBypass          — STREAMING-...; pass-through verbatim
//   T6 MultipartQueriesPassThrough      — ?uploads / ?uploadId / ?partNumber bypass even with mismatched SHA
//   T7 NoExpectedSHAFailsClosed         — ctx with no PayloadSHAFromContext value → 400 (programming error)
//   T8 NonPUTPassesThrough              — GET bypass

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/migrations"
	s3 "github.com/vladoportos/omnirepo/internal/protocol/s3"
	"github.com/vladoportos/omnirepo/internal/protocol/s3/backend"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// interceptEnv builds a real Backend on a TempDir + a working bucket dir,
// returning the backend, the bucket name, and the on-disk dst path for the
// "k1" key the tests use.
type interceptEnv struct {
	backend    *backend.Backend
	bucketName string
	keyPath    string // <DataRoot>/s3/<bucketName>/k1
}

func newInterceptEnv(t *testing.T) *interceptEnv {
	t.Helper()

	dataDir := t.TempDir()
	db, err := metadata.Open(dataDir + "/test.sqlite")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := migrations.Apply(t.Context(), db.Writer); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	locks := storage.NewLocks()
	b := backend.New(dataDir, db, locks)

	const bucket = "test-bucket"
	if err := os.MkdirAll(filepath.Join(b.BucketRoot(bucket)), 0o750); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	return &interceptEnv{
		backend:    b,
		bucketName: bucket,
		keyPath:    filepath.Join(b.BucketRoot(bucket), "k1"),
	}
}

// putReq builds a PUT /s3/test-bucket/k1 request with body and a ctx-bound
// expected payload SHA. This mirrors what SigV4Middleware would have stashed
// downstream of itself in production.
func putReq(t *testing.T, target string, body []byte, expected string, withSHA bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(body))
	if withSHA {
		r = r.WithContext(s3.WithPayloadSHA(r.Context(), expected))
	}
	return r
}

// assertNextNotCalled returns a stub that fails the test if invoked.
func assertNextNotCalled(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("next.ServeHTTP was called but should NOT have been (mismatch path forwards to gofakes3)")
	})
}

// captureNext records whether and what bytes the next handler saw.
type captureNext struct {
	called   int
	gotBody  []byte
	gotQuery string
}

func newCaptureNext() (*captureNext, http.Handler) {
	c := &captureNext{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.called++
		c.gotQuery = r.URL.RawQuery
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			c.gotBody = b
		}
		w.WriteHeader(http.StatusOK)
	})
	return c, h
}

// hexSHA returns hex(sha256(b)).
func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// xmlBody mirrors the AWS error envelope minimum.
type xmlBody struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
}

// TestInterceptPutObject_HexMismatchRejects (T1)
//
// Client signs hex(sha256("")) but PUTs "hello". Intercept must reject with
// 400 XAmzContentSHA256Mismatch, never call next, and leave no file at dst.
func TestInterceptPutObject_HexMismatchRejects(t *testing.T) {
	env := newInterceptEnv(t)

	mw := s3.InterceptPutObjectForTest(env.backend)(assertNextNotCalled(t))

	body := []byte("hello")
	expected := hexSHA(nil) // hex(sha256(""))
	req := putReq(t, "/s3/"+env.bucketName+"/k1", body, expected, true)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", w.Code, w.Body.String())
	}

	var x xmlBody
	if err := xml.Unmarshal(w.Body.Bytes(), &x); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, w.Body.String())
	}
	if x.Code != "XAmzContentSHA256Mismatch" {
		t.Errorf("Code=%q, want XAmzContentSHA256Mismatch", x.Code)
	}

	if _, err := os.Stat(env.keyPath); !os.IsNotExist(err) {
		t.Errorf("dst exists after mismatch (or stat error %v); want IsNotExist", err)
	}
}

// TestInterceptPutObject_PreExistingObjectSurvives (T2 — destructive-overwrite fix)
//
// Pre-stage "v1" at dst. Send mismatched PUT. Assert dst is byte-for-byte
// "v1" after the 400 — gofakes3 was never invoked, so storage.WriteAndRename
// never ran, and the destination file was never touched.
func TestInterceptPutObject_PreExistingObjectSurvives(t *testing.T) {
	env := newInterceptEnv(t)

	const original = "v1"
	if err := os.WriteFile(env.keyPath, []byte(original), 0o640); err != nil {
		t.Fatalf("pre-stage dst: %v", err)
	}

	mw := s3.InterceptPutObjectForTest(env.backend)(assertNextNotCalled(t))

	body := []byte("attacker-supplied-overwrite-bytes")
	expected := hexSHA(nil) // signed empty SHA, but body is not empty
	req := putReq(t, "/s3/"+env.bucketName+"/k1", body, expected, true)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (mismatch); body=%s", w.Code, w.Body.String())
	}

	got, err := os.ReadFile(env.keyPath)
	if err != nil {
		t.Fatalf("read dst after rejection: %v", err)
	}
	if string(got) != original {
		t.Fatalf("DESTRUCTIVE OVERWRITE — dst bytes changed from %q to %q after rejected PUT", original, got)
	}
}

// TestInterceptPutObject_HexMatchPassesThrough (T3)
//
// Body matches signed SHA → next called exactly once and the body it reads
// equals the original bytes (intercept must not consume r.Body without
// re-issuing equivalent bytes).
func TestInterceptPutObject_HexMatchPassesThrough(t *testing.T) {
	env := newInterceptEnv(t)

	cap, next := newCaptureNext()
	mw := s3.InterceptPutObjectForTest(env.backend)(next)

	body := []byte("matching-payload-bytes")
	expected := hexSHA(body)
	req := putReq(t, "/s3/"+env.bucketName+"/k1", body, expected, true)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if cap.called != 1 {
		t.Fatalf("next called %d times, want 1", cap.called)
	}
	if !bytes.Equal(cap.gotBody, body) {
		t.Fatalf("next got body %q, want %q", cap.gotBody, body)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want 200 (next stub returns 200)", w.Code)
	}
}

// TestInterceptPutObject_UnsignedPayloadBypass (T4)
func TestInterceptPutObject_UnsignedPayloadBypass(t *testing.T) {
	env := newInterceptEnv(t)

	cap, next := newCaptureNext()
	mw := s3.InterceptPutObjectForTest(env.backend)(next)

	body := []byte("body-doesnt-need-to-match")
	req := putReq(t, "/s3/"+env.bucketName+"/k1", body, "UNSIGNED-PAYLOAD", true)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if cap.called != 1 {
		t.Fatalf("next called %d times, want 1", cap.called)
	}
	if !bytes.Equal(cap.gotBody, body) {
		t.Fatalf("next got body %q, want %q", cap.gotBody, body)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", w.Code)
	}
}

// TestInterceptPutObject_StreamingSentinelBypass (T5)
func TestInterceptPutObject_StreamingSentinelBypass(t *testing.T) {
	env := newInterceptEnv(t)

	cap, next := newCaptureNext()
	mw := s3.InterceptPutObjectForTest(env.backend)(next)

	body := []byte("chunk-encoded-body-bytes-as-far-as-the-test-cares")
	req := putReq(t, "/s3/"+env.bucketName+"/k1", body,
		"STREAMING-AWS4-HMAC-SHA256-PAYLOAD", true)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if cap.called != 1 {
		t.Fatalf("next called %d times, want 1", cap.called)
	}
	if !bytes.Equal(cap.gotBody, body) {
		t.Fatalf("next got body %q, want %q", cap.gotBody, body)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", w.Code)
	}
}

// TestInterceptPutObject_MultipartQueriesPassThrough (T6)
//
// For each multipart-related query string the intercept must pass the request
// through to next without reading or validating the body — even when the
// declared SHA mismatches.
func TestInterceptPutObject_MultipartQueriesPassThrough(t *testing.T) {
	env := newInterceptEnv(t)

	cases := []struct {
		name  string
		query string
	}{
		{"CreateMultipart", "uploads"},
		{"UploadPart", "uploadId=foo&partNumber=1"},
		{"UploadIdAlone", "uploadId=abc"},
		{"PartNumberAlone", "partNumber=2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap, next := newCaptureNext()
			mw := s3.InterceptPutObjectForTest(env.backend)(next)

			body := []byte("multipart-bytes")
			// Mismatched SHA — but multipart paths must NOT reject.
			expected := hexSHA(nil)
			req := putReq(t, "/s3/"+env.bucketName+"/k1?"+tc.query, body, expected, true)
			w := httptest.NewRecorder()
			mw.ServeHTTP(w, req)

			if cap.called != 1 {
				t.Fatalf("next called %d, want 1 (multipart bypass)", cap.called)
			}
			if w.Code != http.StatusOK {
				t.Errorf("status=%d, want 200; body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(cap.gotQuery, tc.query) && !equalQ(cap.gotQuery, tc.query) {
				t.Errorf("query lost: got=%q, want=%q", cap.gotQuery, tc.query)
			}
		})
	}
}

// equalQ tolerates ampersand-ordered query mismatches (httptest does not
// canonicalize); this is a soft fallback for the multipart cases.
func equalQ(a, b string) bool { return a == b }

// TestInterceptPutObject_NoExpectedSHAFailsClosed (T7)
//
// Programming error: intercept mounted before SigV4Middleware. The ctx has no
// PayloadSHAFromContext value. Intercept must fail closed with 400 — never
// silently bypass.
func TestInterceptPutObject_NoExpectedSHAFailsClosed(t *testing.T) {
	env := newInterceptEnv(t)
	mw := s3.InterceptPutObjectForTest(env.backend)(assertNextNotCalled(t))

	body := []byte("anything")
	req := putReq(t, "/s3/"+env.bucketName+"/k1", body, "", false /* no ctx SHA */)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (fail-closed)", w.Code)
	}
	var x xmlBody
	if err := xml.Unmarshal(w.Body.Bytes(), &x); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if x.Code != "XAmzContentSHA256Mismatch" {
		t.Errorf("Code=%q, want XAmzContentSHA256Mismatch", x.Code)
	}
}

// TestInterceptPutObject_NonPUTPassesThrough (T8)
func TestInterceptPutObject_NonPUTPassesThrough(t *testing.T) {
	env := newInterceptEnv(t)

	cap, next := newCaptureNext()
	mw := s3.InterceptPutObjectForTest(env.backend)(next)

	// GET on an object — intercept must not touch it.
	r := httptest.NewRequest(http.MethodGet, "/s3/"+env.bucketName+"/k1", nil)
	// Even if a SHA happens to be in ctx, GET should bypass.
	r = r.WithContext(s3.WithPayloadSHA(r.Context(), "anything"))
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, r)

	if cap.called != 1 {
		t.Fatalf("next called %d, want 1 (GET bypass)", cap.called)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", w.Code)
	}
}
