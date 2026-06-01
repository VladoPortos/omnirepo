//go:build conformance

package s3conf

// aws-sdk-go-v2 conformance smoke for multipart attribution.
//
// Strategy: drive a real aws-sdk-go-v2 client to create a multipart upload
// via the chi-intercept route (POST /<bucket>/<key>?uploads), then open the
// running app's SQLite DB directly and assert the persisted
// s3_multipart_uploads row carries:
//
//   - initiated_by_s3_key_id  == s3_access_keys.id of the SigV4 AKID used
//   - initiated_by_user_id    == NULL  (the access-key attribution model)
//
// The existing conformance_multipart_pagination_test.go already exercises
// the same chi-intercept path via the aws-sdk-go-v2 paginator — this test
// adds the attribution assertion that covers the same surface end-to-end.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	_ "modernc.org/sqlite"
)

func TestS3MultipartAttribution_Conformance(t *testing.T) {
	fx := bootAppWithS3Bucket(t)
	client := NewClient(t, fx.s3Endpoint, fx.akid, fx.secret, true, 3)

	ctx := context.Background()
	out, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: &fx.bucketName,
		Key:    aws.String("attribution-test"),
	})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	uploadID := aws.ToString(out.UploadId)
	if uploadID == "" {
		t.Fatal("empty UploadId in response")
	}

	// Best-effort cleanup to avoid leaking an in-progress upload into other tests.
	t.Cleanup(func() {
		_, _ = client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
			Bucket:   &fx.bucketName,
			Key:      aws.String("attribution-test"),
			UploadId: &uploadID,
		})
	})

	// Open the running app's DB directly and assert the persisted row.
	dbPath := filepath.Join(fx.dataRoot, "db", "omnirepo.sqlite")
	db, err := sql.Open("sqlite", dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Resolve the s3_access_keys.id for the AKID we used.
	var keyID int64
	if err := db.QueryRow(
		`SELECT id FROM s3_access_keys WHERE access_key_id = ?`, fx.akid,
	).Scan(&keyID); err != nil {
		t.Fatalf("resolve s3_access_keys.id for AKID %q: %v", fx.akid, err)
	}

	var (
		userID    sql.NullInt64
		s3KeyID   sql.NullInt64
		key       string
	)
	if err := db.QueryRow(
		`SELECT initiated_by_user_id, initiated_by_s3_key_id, key
		   FROM s3_multipart_uploads
		  WHERE upload_id = ?`,
		uploadID,
	).Scan(&userID, &s3KeyID, &key); err != nil {
		t.Fatalf("lookup s3_multipart_uploads row for upload %q: %v", uploadID, err)
	}

	// initiated_by_user_id MUST be NULL on the access-key attribution path.
	// Any non-NULL value means the legacy users.id=1 fabrication is back.
	if userID.Valid {
		t.Errorf("initiated_by_user_id = %d; want NULL (audit-finding-#10 regression)", userID.Int64)
	}
	// initiated_by_s3_key_id MUST equal the s3_access_keys.id of the AKID.
	if !s3KeyID.Valid {
		t.Fatalf("initiated_by_s3_key_id is NULL; want %d (chi-intercept attribution failed)", keyID)
	}
	if s3KeyID.Int64 != keyID {
		t.Errorf("initiated_by_s3_key_id = %d; want %d", s3KeyID.Int64, keyID)
	}
	if key != "attribution-test" {
		t.Errorf("persisted key = %q; want %q", key, "attribution-test")
	}
}
