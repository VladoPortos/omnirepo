//go:build probe

// gofakes3 v1.0.0 MultipartBackend interface PRESENT: CreateMultipartUpload, UploadPart, ListMultipartUploads, ListParts, AbortMultipartUpload, CompleteMultipartUpload
//
// Phase 04-01 Wave-0 probe — Assumption A1.
//
// Library fact: gofakes3 has no v1.0.0 git tag upstream. Resolved pseudo-version
// github.com/johannesboyne/gofakes3 v0.0.0-20260208201424-4c385a1f6a73
// is the newest revision on master and contains the MultipartBackend surface
// promised by CLAUDE.md. The Backend interface has EXPANDED beyond what
// 04-01-PLAN.md documented: it now has 12 required methods, not 10.
// Additions the plan did not list:
//   - ForceDeleteBucket(name string) error
//   - CopyObject(srcBucket, srcKey, dstBucket, dstKey string, meta map[string]string) (CopyObjectResult, error)
// and PutObject now takes an extra *PutConditions parameter for conditional writes.
//
// Plan 06 (S3 Stream A) CAN use the embedded MultipartBackend surface. No
// need to front gofakes3 with a bespoke multipart HTTP handler.
//
// File named probe_test.go (NOT _probe_test.go) because the Go build system
// silently ignores files starting with '_' or '.'; documented as a Rule-1
// deviation in 04-01-SUMMARY.md.

package backend_probe

import (
	"io"
	"testing"

	"github.com/johannesboyne/gofakes3"
)

// stubBackend implements the non-optional gofakes3.Backend method set so the
// compiler verifies every method name and signature is still present.
type stubBackend struct{}

func (stubBackend) ListBuckets() ([]gofakes3.BucketInfo, error) { return nil, nil }
func (stubBackend) ListBucket(name string, prefix *gofakes3.Prefix, page gofakes3.ListBucketPage) (*gofakes3.ObjectList, error) {
	return nil, nil
}
func (stubBackend) CreateBucket(name string) error         { return nil }
func (stubBackend) BucketExists(name string) (bool, error) { return false, nil }
func (stubBackend) DeleteBucket(name string) error         { return nil }
func (stubBackend) ForceDeleteBucket(name string) error    { return nil }
func (stubBackend) GetObject(bucketName, objectName string, r *gofakes3.ObjectRangeRequest) (*gofakes3.Object, error) {
	return nil, nil
}
func (stubBackend) HeadObject(bucketName, objectName string) (*gofakes3.Object, error) {
	return nil, nil
}
func (stubBackend) DeleteObject(bucketName, objectName string) (gofakes3.ObjectDeleteResult, error) {
	return gofakes3.ObjectDeleteResult{}, nil
}
func (stubBackend) PutObject(bucketName, key string, meta map[string]string, input io.Reader, size int64, conditions *gofakes3.PutConditions) (gofakes3.PutObjectResult, error) {
	return gofakes3.PutObjectResult{}, nil
}
func (stubBackend) DeleteMulti(bucketName string, objects ...string) (gofakes3.MultiDeleteResult, error) {
	return gofakes3.MultiDeleteResult{}, nil
}
func (stubBackend) CopyObject(srcBucket, srcKey, dstBucket, dstKey string, meta map[string]string) (gofakes3.CopyObjectResult, error) {
	return gofakes3.CopyObjectResult{}, nil
}

// stubMultipartBackend adds the optional MultipartBackend surface.
type stubMultipartBackend struct{ stubBackend }

func (stubMultipartBackend) CreateMultipartUpload(bucket, object string, meta map[string]string) (gofakes3.UploadID, error) {
	return "", nil
}
func (stubMultipartBackend) UploadPart(bucket, object string, id gofakes3.UploadID, partNumber int, contentLength int64, input io.Reader) (etag string, err error) {
	return "", nil
}
func (stubMultipartBackend) ListMultipartUploads(bucket string, marker *gofakes3.UploadListMarker, prefix gofakes3.Prefix, limit int64) (*gofakes3.ListMultipartUploadsResult, error) {
	return nil, nil
}
func (stubMultipartBackend) ListParts(bucket, object string, uploadID gofakes3.UploadID, marker int, limit int64) (*gofakes3.ListMultipartUploadPartsResult, error) {
	return nil, nil
}
func (stubMultipartBackend) AbortMultipartUpload(bucket, object string, id gofakes3.UploadID) error {
	return nil
}
func (stubMultipartBackend) CompleteMultipartUpload(bucket, object string, id gofakes3.UploadID, input *gofakes3.CompleteMultipartUploadRequest) (versionID gofakes3.VersionID, etag string, err error) {
	return "", "", nil
}

func TestGofakes3Interfaces(t *testing.T) {
	var _ gofakes3.Backend = stubBackend{}
	var _ gofakes3.Backend = stubMultipartBackend{}
	var _ gofakes3.MultipartBackend = stubMultipartBackend{}
}
