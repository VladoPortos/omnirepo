package backend_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
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

// fixtureCreateMPU is a thin wrapper that drives the post-Plan 02-04
// actor-aware path (CreateMultipartUploadCtx) using a stable test-fixture
// S3 key id. Existing tests in this file used the legacy
// CreateMultipartUpload entry point, which is now a defensive shim that
// always returns gofakes3.ErrInternal — production traffic flows through
// the chi-intercept-driven CreateMultipartUploadCtx (S3HARD-06, audit
// finding #10).
//
// The helper inserts a fixture s3_access_keys row on first call and
// memoizes the id on the fixture, so a single test run uses one key.
func fixtureCreateMPU(t *testing.T, f *fixture, bucket, object string, meta map[string]string) (gofakes3.UploadID, error) {
	t.Helper()
	if f.s3KeyID == 0 {
		res, err := f.db.Writer.ExecContext(context.Background(),
			`INSERT INTO s3_access_keys(project_id, label, access_key_id, secret_enc, created_by_user_id)
			 VALUES (?, 'fixture-mpu', 'AKIDMPU', X'00', 1)`, f.projectID,
		)
		if err != nil {
			t.Fatalf("seed s3_access_keys for fixture: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("s3_access_keys lastid: %v", err)
		}
		f.s3KeyID = id
	}
	return f.b.CreateMultipartUploadCtx(context.Background(), bucket, object, meta, &f.s3KeyID)
}

// -- Task 2 multipart tests ------------------------------------------------

func TestCreateMultipartUpload_SetsUpDiskAndRow(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, err := fixtureCreateMPU(t, f,"bucket1", "big.bin", map[string]string{"Content-Type": "application/zip"})
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
	id, err := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
	id, err := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
	id, err := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
	id, err := fixtureCreateMPU(t, f,"bucket1", "k", map[string]string{"Content-Type": "application/octet-stream"})
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
	id, _ := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
	id, _ := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
	id, _ := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
	id, _ := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
	id, _ := fixtureCreateMPU(t, f,"bucket1", "k", nil)
	// Force initiated_at into the past.
	if _, err := f.db.Writer.ExecContext(context.Background(),
		`UPDATE s3_multipart_uploads SET initiated_at='2020-01-01T00:00:00.000Z' WHERE upload_id=?`, string(id)); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	if _, _, err := f.b.SweepOrphanMultiparts(context.Background(), cutoff); err != nil {
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
	id, _ := fixtureCreateMPU(t, f,"bucket1", "k", nil)
	cutoff := time.Now().Add(-24 * time.Hour)
	if _, _, err := f.b.SweepOrphanMultiparts(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.NewS3MultipartRepo(f.db).FindUpload(context.Background(), string(id)); err != nil {
		t.Fatalf("fresh upload swept: %v", err)
	}
}

// -- Plan 02-05 ListMultipartUploads / ListParts pagination tests ----------
// (S3HARD-09 / S3HARD-10)

// seedMPU creates n multipart uploads in `bucket` with keys k1..kn (no body)
// and returns the upload IDs in lexicographic key order.
func seedMPU(t *testing.T, f *fixture, bucket string, keys []string) map[string]gofakes3.UploadID {
	t.Helper()
	out := map[string]gofakes3.UploadID{}
	for _, k := range keys {
		id, err := fixtureCreateMPU(t, f,bucket, k, nil)
		if err != nil {
			t.Fatalf("seed mpu %s: %v", k, err)
		}
		out[k] = id
	}
	return out
}

func TestListMultipartUploads_PaginationTruncation(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	ids := seedMPU(t, f, "bkt", []string{"k1", "k2", "k3"})

	// Page 1: limit=2 → returns 2 results, IsTruncated=true.
	page1, err := f.b.ListMultipartUploads("bkt", nil, gofakes3.Prefix{}, 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Uploads) != 2 {
		t.Fatalf("page1: want 2 uploads, got %d", len(page1.Uploads))
	}
	if !page1.IsTruncated {
		t.Fatalf("page1: want IsTruncated=true")
	}
	if page1.NextKeyMarker != "k2" {
		t.Fatalf("page1 NextKeyMarker: want k2, got %q", page1.NextKeyMarker)
	}
	if page1.NextUploadIDMarker != ids["k2"] {
		t.Fatalf("page1 NextUploadIDMarker: want %s, got %s", ids["k2"], page1.NextUploadIDMarker)
	}

	// Page 2: cursor at (k2, ids[k2]) → returns k3 only, IsTruncated=false.
	marker := &gofakes3.UploadListMarker{Object: page1.NextKeyMarker, UploadID: page1.NextUploadIDMarker}
	page2, err := f.b.ListMultipartUploads("bkt", marker, gofakes3.Prefix{}, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Uploads) != 1 {
		t.Fatalf("page2: want 1 upload, got %d", len(page2.Uploads))
	}
	if page2.IsTruncated {
		t.Fatalf("page2: want IsTruncated=false")
	}
	if page2.NextKeyMarker != "" {
		t.Fatalf("page2 NextKeyMarker: want empty, got %q", page2.NextKeyMarker)
	}
	if page2.Uploads[0].Key != "k3" {
		t.Fatalf("page2: want k3, got %s", page2.Uploads[0].Key)
	}
}

func TestListParts_PaginationTruncation(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	id, err := fixtureCreateMPU(t, f,"bkt", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Seed 5 parts directly via the repo (avoid the 5 MiB minimum-size /
	// 10000-part-limit overhead of UploadPart).
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		mpr := metadata.NewS3MultipartRepo(f.db)
		for i := 1; i <= 5; i++ {
			if err := mpr.AddPart(context.Background(), tx, &metadata.S3MultipartPart{
				UploadID: string(id), PartNumber: i, SizeBytes: int64(i), MD5: "m",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed parts: %v", err)
	}

	// Page 1: marker=0 limit=3 → 3 parts, IsTruncated=true, NextPartNumberMarker=3.
	res1, err := f.b.ListParts("bkt", "k", id, 0, 3)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(res1.Parts) != 3 {
		t.Fatalf("page1: want 3, got %d", len(res1.Parts))
	}
	if !res1.IsTruncated {
		t.Fatalf("page1: want IsTruncated=true")
	}
	if res1.NextPartNumberMarker != 3 {
		t.Fatalf("page1 NextPartNumberMarker: want 3, got %d", res1.NextPartNumberMarker)
	}

	// Page 2: marker=3 limit=3 → 2 parts (4, 5), IsTruncated=false.
	res2, err := f.b.ListParts("bkt", "k", id, 3, 3)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(res2.Parts) != 2 {
		t.Fatalf("page2: want 2, got %d", len(res2.Parts))
	}
	if res2.IsTruncated {
		t.Fatalf("page2: want IsTruncated=false")
	}
	if res2.Parts[0].PartNumber != 4 || res2.Parts[1].PartNumber != 5 {
		t.Fatalf("page2 parts: %+v", res2.Parts)
	}
}

func TestListMultipartUploads_AppliesAWSDefault(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	// Seed 3 uploads. limit=0 → SDK default 1000 → all 3 returned, no truncation.
	seedMPU(t, f, "bkt", []string{"k1", "k2", "k3"})
	res, err := f.b.ListMultipartUploads("bkt", nil, gofakes3.Prefix{}, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Uploads) != 3 {
		t.Fatalf("want 3 uploads under default, got %d", len(res.Uploads))
	}
	if res.IsTruncated {
		t.Fatalf("want IsTruncated=false under default")
	}
	if res.MaxUploads != 1000 {
		t.Fatalf("want MaxUploads=1000 (clamped default), got %d", res.MaxUploads)
	}
}

func TestListParts_AppliesAWSDefault(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	id, err := fixtureCreateMPU(t, f,"bkt", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		mpr := metadata.NewS3MultipartRepo(f.db)
		for i := 1; i <= 3; i++ {
			if err := mpr.AddPart(context.Background(), tx, &metadata.S3MultipartPart{
				UploadID: string(id), PartNumber: i, SizeBytes: int64(i), MD5: "m",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := f.b.ListParts("bkt", "k", id, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Parts) != 3 {
		t.Fatalf("want 3 parts under default, got %d", len(res.Parts))
	}
	if res.IsTruncated {
		t.Fatal("want IsTruncated=false under default")
	}
	if res.MaxParts != 1000 {
		t.Fatalf("want MaxParts=1000 (clamped default), got %d", res.MaxParts)
	}
}

func TestListMultipartUploads_NoTruncationOnExactMatch(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	// Off-by-one regression guard: 2 uploads, limit=2 → no truncation.
	seedMPU(t, f, "bkt", []string{"k1", "k2"})
	res, err := f.b.ListMultipartUploads("bkt", nil, gofakes3.Prefix{}, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Uploads) != 2 {
		t.Fatalf("want 2 uploads, got %d", len(res.Uploads))
	}
	if res.IsTruncated {
		t.Fatal("want IsTruncated=false on exact-match (off-by-one regression)")
	}
	if res.NextKeyMarker != "" {
		t.Fatalf("want empty NextKeyMarker, got %q", res.NextKeyMarker)
	}
}

// Ensure the Range reader used during GetObject isn't confused by empty Range.
func TestGetObject_NoRangeAfterMultipartComplete(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	id, _ := fixtureCreateMPU(t, f,"bucket1", "k", nil)
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
