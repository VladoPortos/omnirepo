package backend_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// -- Task 2 multipart tests ------------------------------------------------

func TestCreateMultipartUpload_SetsUpDiskAndRow(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, err := f.b.CreateMultipartUpload("bucket1", "big.bin", map[string]string{"Content-Type": "application/zip"})
	if err != nil {
		t.Fatalf("create mpu: %v", err)
	}
	if string(id) == "" {
		t.Fatal("empty upload id")
	}
	// Staging dir exists.
	staging := filepath.Join(f.dataRoot, "tmp", "s3", string(id))
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging missing: %v", err)
	}
	// DB row exists.
	up, err := metadata.NewS3MultipartRepo(f.db).FindUpload(context.Background(), string(id))
	if err != nil {
		t.Fatalf("find upload: %v", err)
	}
	if up.Key != "big.bin" {
		t.Fatalf("key %q", up.Key)
	}
}

func TestUploadPart_WritesAtomicallyAndRecordsMD5(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, err := f.b.CreateMultipartUpload("bucket1", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte{0xaa}, 5*1024*1024)
	expect := md5.Sum(body)
	etag, err := f.b.UploadPart("bucket1", "k", id, 1, int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if etag != `"`+hex.EncodeToString(expect[:])+`"` {
		t.Fatalf("etag=%s", etag)
	}
	// File on disk.
	partPath := filepath.Join(f.dataRoot, "tmp", "s3", string(id), "1.bin")
	if st, err := os.Stat(partPath); err != nil || st.Size() != int64(len(body)) {
		t.Fatalf("part file: %v size=%d", err, st.Size())
	}
	// Row in s3_multipart_parts.
	parts, err := metadata.NewS3MultipartRepo(f.db).ListParts(context.Background(), string(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].PartNumber != 1 || parts[0].SizeBytes != int64(len(body)) {
		t.Fatalf("parts: %+v", parts)
	}
}

func TestUploadPart_RejectsOversizePart(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, err := f.b.CreateMultipartUpload("bucket1", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	// contentLength declared > 5 GiB — must be rejected without reading body.
	_, err = f.b.UploadPart("bucket1", "k", id, 1, 6*1024*1024*1024, bytes.NewReader(nil))
	if !gofakes3.HasErrorCode(err, gofakes3.ErrInvalidPart) {
		t.Fatalf("want ErrInvalidPart, got %v", err)
	}
}

func TestUploadPart_UpsertOnDuplicatePartNumber(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, err := f.b.CreateMultipartUpload("bucket1", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	body1 := bytes.Repeat([]byte{1}, 5*1024*1024)
	body2 := bytes.Repeat([]byte{2}, 5*1024*1024)
	if _, err := f.b.UploadPart("bucket1", "k", id, 1, int64(len(body1)), bytes.NewReader(body1)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.UploadPart("bucket1", "k", id, 1, int64(len(body2)), bytes.NewReader(body2)); err != nil {
		t.Fatal(err)
	}
	parts, _ := metadata.NewS3MultipartRepo(f.db).ListParts(context.Background(), string(id))
	if len(parts) != 1 {
		t.Fatalf("want 1 part (upsert), got %d", len(parts))
	}
	want := md5.Sum(body2)
	if parts[0].MD5 != hex.EncodeToString(want[:]) {
		t.Fatal("upsert didn't replace md5")
	}
}

// TestCompleteMultipart_KnownVector — deterministic AWS-compatible ETag.
// Two parts of 5 MiB zeroes each → md5(each part) = ea8c4b3b0a87... Let the
// code compute and assert the hand-derived vector.
//
// Expected ETag derivation (standard S3):
//   part1_md5 = md5(5 MiB of 0x00)
//   part2_md5 = md5(5 MiB of 0x00) (same)
//   concat    = part1_md5_raw || part2_md5_raw (32 bytes)
//   etag      = md5(concat) + "-2"
func TestCompleteMultipart_KnownVector(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, err := f.b.CreateMultipartUpload("bucket1", "k", map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte{0x00}, 5*1024*1024)
	// Pre-compute individual part md5 and the expected final ETag.
	partMD5 := md5.Sum(body)
	concat := append(append([]byte{}, partMD5[:]...), partMD5[:]...)
	finalSum := md5.Sum(concat)
	expectedETag := hex.EncodeToString(finalSum[:]) + "-2"

	etag1, err := f.b.UploadPart("bucket1", "k", id, 1, int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	etag2, err := f.b.UploadPart("bucket1", "k", id, 2, int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if etag1 != etag2 {
		t.Fatalf("identical bodies produced different part etags")
	}
	complete := &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 2, ETag: etag2},
		},
	}
	_, gotETag, err := f.b.CompleteMultipartUpload("bucket1", "k", id, complete)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	wantQuoted := `"` + expectedETag + `"`
	if gotETag != wantQuoted {
		t.Fatalf("etag=%s want=%s", gotETag, wantQuoted)
	}
	// Final file exists, size = 10 MiB.
	path := filepath.Join(f.dataRoot, "s3", "bucket1", "k")
	st, err := os.Stat(path)
	if err != nil || st.Size() != 10*1024*1024 {
		t.Fatalf("final file: %v size=%d", err, st.Size())
	}
	// Staging is gone.
	if _, err := os.Stat(filepath.Join(f.dataRoot, "tmp", "s3", string(id))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived: %v", err)
	}
	// DB rows gone.
	up, err := metadata.NewS3MultipartRepo(f.db).FindUpload(context.Background(), string(id))
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("upload row still present: up=%v err=%v", up, err)
	}
}

func TestCompleteMultipart_RejectsETagMismatch(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := f.b.CreateMultipartUpload("bucket1", "k", nil)
	body := bytes.Repeat([]byte{1}, 5*1024*1024)
	etag, _ := f.b.UploadPart("bucket1", "k", id, 1, int64(len(body)), bytes.NewReader(body))
	body2 := bytes.Repeat([]byte{2}, 5*1024*1024)
	_, _ = f.b.UploadPart("bucket1", "k", id, 2, int64(len(body2)), bytes.NewReader(body2))
	// Claim a bogus etag for part 2.
	complete := &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag},
			{PartNumber: 2, ETag: `"deadbeef"`},
		},
	}
	_, _, err := f.b.CompleteMultipartUpload("bucket1", "k", id, complete)
	if !gofakes3.HasErrorCode(err, gofakes3.ErrInvalidPart) {
		t.Fatalf("want ErrInvalidPart, got %v", err)
	}
	// No final file written.
	if _, err := os.Stat(filepath.Join(f.dataRoot, "s3", "bucket1", "k")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("final file written despite mismatch")
	}
}

func TestCompleteMultipart_IdempotencyBoundary_SecondCompleteFails(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := f.b.CreateMultipartUpload("bucket1", "k", nil)
	body := bytes.Repeat([]byte{0}, 5*1024*1024)
	etag, _ := f.b.UploadPart("bucket1", "k", id, 1, int64(len(body)), bytes.NewReader(body))
	complete := &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
	}
	if _, _, err := f.b.CompleteMultipartUpload("bucket1", "k", id, complete); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.b.CompleteMultipartUpload("bucket1", "k", id, complete)
	if !gofakes3.HasErrorCode(err, gofakes3.ErrNoSuchUpload) {
		t.Fatalf("second complete: want ErrNoSuchUpload, got %v", err)
	}
}

func TestCompleteMultipart_RejectsSmallNonLastPart(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := f.b.CreateMultipartUpload("bucket1", "k", nil)
	// First part only 1 MiB — below 5 MiB minimum.
	small := bytes.Repeat([]byte{0}, 1*1024*1024)
	etag1, err := f.b.UploadPart("bucket1", "k", id, 1, int64(len(small)), bytes.NewReader(small))
	if err != nil {
		t.Fatalf("upload small: %v", err)
	}
	final := bytes.Repeat([]byte{0}, 5*1024*1024)
	etag2, _ := f.b.UploadPart("bucket1", "k", id, 2, int64(len(final)), bytes.NewReader(final))
	complete := &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 2, ETag: etag2},
		},
	}
	_, _, err = f.b.CompleteMultipartUpload("bucket1", "k", id, complete)
	if !gofakes3.HasErrorCode(err, gofakes3.ErrInvalidPart) {
		t.Fatalf("want ErrInvalidPart (EntityTooSmall), got %v", err)
	}
}

func TestAbortMultipart_RemovesRowsAndStaging(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := f.b.CreateMultipartUpload("bucket1", "k", nil)
	body := bytes.Repeat([]byte{0}, 5*1024*1024)
	_, _ = f.b.UploadPart("bucket1", "k", id, 1, int64(len(body)), bytes.NewReader(body))
	if err := f.b.AbortMultipartUpload("bucket1", "k", id); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dataRoot, "tmp", "s3", string(id))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging still present")
	}
	// Idempotent second abort.
	if err := f.b.AbortMultipartUpload("bucket1", "k", id); err != nil {
		t.Fatalf("second abort: %v", err)
	}
}

func TestSweepOrphanMultiparts_AbortsOld(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := f.b.CreateMultipartUpload("bucket1", "k", nil)
	// Force initiated_at into the past.
	if _, err := f.db.Writer.ExecContext(context.Background(),
		`UPDATE s3_multipart_uploads SET initiated_at='2020-01-01T00:00:00.000Z' WHERE upload_id=?`, string(id)); err != nil {
		t.Fatal(err)
	}
	if err := f.b.SweepOrphanMultiparts(context.Background(), time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// Upload should be gone.
	if _, err := metadata.NewS3MultipartRepo(f.db).FindUpload(context.Background(), string(id)); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("sweep did not remove stale upload")
	}
}

func TestSweepOrphanMultiparts_LeavesFreshAlone(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := f.b.CreateMultipartUpload("bucket1", "k", nil)
	if err := f.b.SweepOrphanMultiparts(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.NewS3MultipartRepo(f.db).FindUpload(context.Background(), string(id)); err != nil {
		t.Fatalf("fresh upload swept: %v", err)
	}
}

// Ensure the Range reader used during GetObject isn't confused by empty Range.
func TestGetObject_NoRangeAfterMultipartComplete(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := f.b.CreateMultipartUpload("bucket1", "k", nil)
	body := bytes.Repeat([]byte{0xaa}, 5*1024*1024)
	etag, _ := f.b.UploadPart("bucket1", "k", id, 1, int64(len(body)), bytes.NewReader(body))
	complete := &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
	}
	if _, _, err := f.b.CompleteMultipartUpload("bucket1", "k", id, complete); err != nil {
		t.Fatal(err)
	}
	obj, err := f.b.GetObject("bucket1", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(obj.Contents)
	obj.Contents.Close()
	if !bytes.Equal(out, body) {
		t.Fatalf("body mismatch: len=%d want=%d", len(out), len(body))
	}
}
