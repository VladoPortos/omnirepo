package backend_test

// Plan 02-04 Task 1 — CreateMultipartUploadCtx attribution unit tests.
//
// CreateMultipartUploadCtx(ctx, bucket, object, meta, actorS3KeyID) is the
// new actor-aware entry point invoked by interceptCreateMultipartUpload
// (chi-side intercept). Persisted s3_multipart_uploads rows MUST carry
// initiated_by_s3_key_id == actorS3KeyID and initiated_by_user_id == nil.
//
// The legacy CreateMultipartUpload now returns gofakes3.ErrInternal — it's
// a defensive safety-net stub that should never fire in production because
// the chi intercept hijacks ?uploads POST before gofakes3.

import (
	"context"
	"errors"
	"testing"

	"github.com/johannesboyne/gofakes3"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// seedS3Key inserts a fixture s3_access_keys row tied to the fixture
// project and returns its primary key id.
func seedS3Key(t *testing.T, f *fixture) int64 {
	t.Helper()
	res, err := f.db.Writer.ExecContext(context.Background(),
		`INSERT INTO s3_access_keys(project_id, label, access_key_id, secret_enc, created_by_user_id)
		 VALUES (?, 'test-key', 'AKIDTEST123', X'00', 1)`,
		f.projectID,
	)
	if err != nil {
		t.Fatalf("seed s3_access_keys: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// TestCreateMultipartUploadCtx_AcceptsActorS3KeyID is the happy-path
// attribution test: the persisted row carries the actor's S3 key id in
// initiated_by_s3_key_id and NULL in initiated_by_user_id.
func TestCreateMultipartUploadCtx_AcceptsActorS3KeyID(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	keyID := seedS3Key(t, f)

	id, err := f.b.CreateMultipartUploadCtx(
		context.Background(), "bucket1", "k", map[string]string{"Content-Type": "text/plain"}, &keyID,
	)
	if err != nil {
		t.Fatalf("CreateMultipartUploadCtx: %v", err)
	}
	if string(id) == "" {
		t.Fatal("empty upload id")
	}

	// Inspect the persisted row.
	repo := metadata.NewS3MultipartRepo(f.db)
	up, err := repo.FindUpload(context.Background(), string(id))
	if err != nil {
		t.Fatalf("FindUpload: %v", err)
	}
	if up.InitiatedByS3KeyID == nil {
		t.Fatalf("InitiatedByS3KeyID nil; want %d", keyID)
	}
	if *up.InitiatedByS3KeyID != keyID {
		t.Fatalf("InitiatedByS3KeyID = %d; want %d", *up.InitiatedByS3KeyID, keyID)
	}
	if up.InitiatedByUserID != nil {
		t.Fatalf("InitiatedByUserID = %d; want nil (S3-key-attributed row)", *up.InitiatedByUserID)
	}
}

// TestCreateMultipartUploadCtx_NilS3KeyIDReturnsError is the fail-closed
// guard: if the chi intercept ever invokes the backend method without an
// actor (programming error), it returns an error rather than persisting
// an unattributed row.
func TestCreateMultipartUploadCtx_NilS3KeyIDReturnsError(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.CreateMultipartUploadCtx(
		context.Background(), "bucket1", "k", nil, nil,
	); err == nil {
		t.Fatal("CreateMultipartUploadCtx with nil actorS3KeyID: want error, got nil")
	}
}

// TestCreateMultipartUpload_LegacyShimReturnsErrInternal pins the
// safety-net behavior on the legacy gofakes3-driven path. Production
// requests never hit it (chi intercept handles ?uploads POST), but if
// gofakes3 ever does dispatch to it the response is ErrInternal — never
// a silently-attributed row.
func TestCreateMultipartUpload_LegacyShimReturnsErrInternal(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	_, err := f.b.CreateMultipartUpload("bucket1", "k", nil)
	if err == nil {
		t.Fatal("legacy CreateMultipartUpload: want error, got nil")
	}
	// Accept the gofakes3 ErrInternal sentinel — its shape lets gofakes3
	// emit a 500 InternalError envelope to the rare client that somehow
	// reached this path.
	var asErr gofakes3.Error
	if !errors.As(err, &asErr) {
		t.Fatalf("err = %v; want a gofakes3 error", err)
	}
}
