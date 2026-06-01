package s3_test

// interceptCreateMultipartUpload middleware tests.
//
// Coverage matrix:
//
//   T1 AttributesS3KeyID         — POST /bucket/key?uploads → next NEVER called,
//                                  response is AWS-shape <InitiateMultipartUploadResult>,
//                                  persisted s3_multipart_uploads row has
//                                  initiated_by_s3_key_id == fixture id and
//                                  initiated_by_user_id == nil.
//   T2 RejectsMissingActor       — ctx with no actor.S3KeyID → 400 (or 403)
//                                  AWS-shape error envelope; no row inserted.
//   T3 PassesThroughNonUploads   — POST without ?uploads bypass to next.
//   T4 PassesThroughNonPOST      — PUT /bucket/key?uploads (single-shot
//                                  PutObject path — interceptPutObject owns
//                                  it) bypass to next; intercept does not
//                                  insert a multipart row.

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	s3 "github.com/vladoportos/omnirepo/internal/protocol/s3"
)

// initiateXML mirrors AWS S3's InitiateMultipartUploadResult envelope.
type initiateXML struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

// seedFullEnv populates project/user/bucket DB rows on top of the bare
// interceptEnv that newInterceptEnv builds (which only creates the on-disk
// bucket directory). Returns the s3_access_keys.id of a fixture key that
// the intercept's CreateMultipartUploadCtx call will read off ctx.
//
// The chi intercept's b.findBucketID lookup requires a real s3_buckets row
// (joined to projects WHERE deleted_at IS NULL). The bucket that
// newInterceptEnv created on disk has no DB row by default, so we add
// one here scoped to a freshly-seeded project.
func seedFullEnv(t *testing.T, env *interceptEnv) int64 {
	t.Helper()
	ctx := context.Background()
	// Bootstrap super-admin user (FK target for s3_access_keys.created_by_user_id).
	if _, err := env.backend.DB.Writer.ExecContext(ctx,
		`INSERT INTO users(id, login, email, password_hash) VALUES (1, 'admin', 'admin@x', 'x')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Project that owns the bucket.
	res, err := env.backend.DB.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES ('intercept-proj')`,
	)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	pid, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("project lastid: %v", err)
	}
	// Bucket DB row.
	if _, err := env.backend.DB.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES (?, ?)`,
		env.bucketName, pid,
	); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	// Fixture s3_access_keys row.
	res, err = env.backend.DB.Writer.ExecContext(ctx,
		`INSERT INTO s3_access_keys(project_id, label, access_key_id, secret_enc, created_by_user_id)
		 VALUES (?, 'fixture', 'AKIDFIX', X'00', 1)`, pid,
	)
	if err != nil {
		t.Fatalf("seed s3_access_keys: %v", err)
	}
	keyID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("s3_access_keys lastid: %v", err)
	}
	return keyID
}

// uploadsReq builds POST /s3/<bucket>/<key>?uploads with the actor seeded
// on ctx via auth.WithActor. The S3KeyID is the only relevant field for
// the multipart-attribution path.
func uploadsReq(t *testing.T, env *interceptEnv, key string, keyID *int64) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost,
		"/s3/"+env.bucketName+"/"+key+"?uploads",
		bytes.NewReader(nil))
	if keyID != nil {
		ctx := auth.WithActor(r.Context(), auth.Actor{
			Kind:    auth.ActorKindS3Key,
			S3KeyID: keyID,
		})
		r = r.WithContext(ctx)
	}
	return r
}

// TestInterceptCreateMultipart_AttributesS3KeyID (T1)
func TestInterceptCreateMultipart_AttributesS3KeyID(t *testing.T) {
	env := newInterceptEnv(t)
	keyID := seedFullEnv(t, env)

	mw := s3.InterceptCreateMultipartUploadForTest(env.backend)(assertNextNotCalled(t))

	const objectKey = "k1"
	req := uploadsReq(t, env, objectKey, &keyID)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}

	// Parse the AWS-shape response.
	var got initiateXML
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("xml.Unmarshal: %v\nraw=%s", err, w.Body.String())
	}
	if got.Bucket != env.bucketName {
		t.Errorf("Bucket=%q want %q", got.Bucket, env.bucketName)
	}
	if got.Key != objectKey {
		t.Errorf("Key=%q want %q", got.Key, objectKey)
	}
	if got.UploadID == "" {
		t.Fatal("UploadId empty")
	}

	// Inspect persisted row: must carry initiated_by_s3_key_id and have
	// initiated_by_user_id nil.
	repo := metadata.NewS3MultipartRepo(env.backend.DB)
	up, err := repo.FindUpload(context.Background(), got.UploadID)
	if err != nil {
		t.Fatalf("FindUpload: %v", err)
	}
	if up.InitiatedByS3KeyID == nil || *up.InitiatedByS3KeyID != keyID {
		t.Errorf("InitiatedByS3KeyID=%v want %d", up.InitiatedByS3KeyID, keyID)
	}
	if up.InitiatedByUserID != nil {
		t.Errorf("InitiatedByUserID=%v want nil (S3-key-attributed row)", *up.InitiatedByUserID)
	}
}

// TestInterceptCreateMultipart_RejectsMissingActor (T2)
//
// Programming error: SigV4Middleware not run, or actor.S3KeyID nil. The
// intercept must fail closed — no multipart row inserted, AWS-shape error
// envelope returned. We accept either 400 or 403 for the status code (the
// exact error envelope is nominated by sigv4.WriteError mapping).
func TestInterceptCreateMultipart_RejectsMissingActor(t *testing.T) {
	env := newInterceptEnv(t)

	mw := s3.InterceptCreateMultipartUploadForTest(env.backend)(assertNextNotCalled(t))

	req := uploadsReq(t, env, "k1", nil) // no actor on ctx
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("status=%d want 4xx (fail-closed); body=%s", w.Code, w.Body.String())
	}

	// Body should be an AWS-shape <Error> envelope.
	var x xmlBody
	if err := xml.Unmarshal(w.Body.Bytes(), &x); err != nil {
		t.Fatalf("xml.Unmarshal: %v\nraw=%s", err, w.Body.String())
	}
	if x.Code == "" {
		t.Errorf("Code=%q want non-empty AWS error code", x.Code)
	}

	// No multipart row should have been inserted.
	var n int
	if err := env.backend.DB.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM s3_multipart_uploads`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("count=%d want 0 (no row on fail-closed)", n)
	}
}

// TestInterceptCreateMultipart_PassesThroughNonUploads (T3)
//
// POST without ?uploads (e.g. CompleteMultipartUpload's POST?uploadId=...
// flow gofakes3 owns) must pass through to next without inserting a
// multipart row.
func TestInterceptCreateMultipart_PassesThroughNonUploads(t *testing.T) {
	env := newInterceptEnv(t)

	cap, next := newCaptureNext()
	mw := s3.InterceptCreateMultipartUploadForTest(env.backend)(next)

	// POST /bucket/k1?uploadId=foo — different multipart route gofakes3 handles.
	r := httptest.NewRequest(http.MethodPost,
		"/s3/"+env.bucketName+"/k1?uploadId=foo", bytes.NewReader([]byte("x")))
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, r)

	if cap.called != 1 {
		t.Fatalf("next called %d, want 1 (non-uploads POST bypass)", cap.called)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want 200 (next stub)", w.Code)
	}

	var n int
	_ = env.backend.DB.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM s3_multipart_uploads`,
	).Scan(&n)
	if n != 0 {
		t.Errorf("count=%d, want 0 (intercept must not insert on non-uploads bypass)", n)
	}
}

// TestInterceptCreateMultipart_PassesThroughNonPOST (T4)
//
// PUT (or GET) on the same path with ?uploads must NOT be intercepted —
// only POST creates a multipart upload per AWS spec. Single-shot PUT is
// owned by interceptPutObject.
func TestInterceptCreateMultipart_PassesThroughNonPOST(t *testing.T) {
	env := newInterceptEnv(t)

	cap, next := newCaptureNext()
	mw := s3.InterceptCreateMultipartUploadForTest(env.backend)(next)

	r := httptest.NewRequest(http.MethodPut,
		"/s3/"+env.bucketName+"/k1?uploads", bytes.NewReader([]byte("ignored")))
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, r)

	if cap.called != 1 {
		t.Fatalf("next called %d, want 1 (non-POST bypass)", cap.called)
	}

	var n int
	_ = env.backend.DB.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM s3_multipart_uploads`,
	).Scan(&n)
	if n != 0 {
		t.Errorf("count=%d, want 0", n)
	}
}

// TestInterceptCreateMultipart_KeyExtractionMatchesAWS pins key
// extraction for nested keys: POST /s3/<bucket>/<a>/<b>/<c>?uploads must
// persist key="a/b/c" (everything after the bucket segment).
func TestInterceptCreateMultipart_KeyExtractionMatchesAWS(t *testing.T) {
	env := newInterceptEnv(t)
	keyID := seedFullEnv(t, env)

	mw := s3.InterceptCreateMultipartUploadForTest(env.backend)(assertNextNotCalled(t))

	const objectKey = "a/b/c.txt"
	req := uploadsReq(t, env, objectKey, &keyID)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var got initiateXML
	if err := xml.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Key != objectKey {
		t.Errorf("Key=%q want %q (nested key extraction)", got.Key, objectKey)
	}
	if !strings.Contains(w.Body.String(), "<Bucket>"+env.bucketName+"</Bucket>") {
		t.Errorf("body missing <Bucket>%s</Bucket>; got %s", env.bucketName, w.Body.String())
	}
}
