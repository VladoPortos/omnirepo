package backend_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/johannesboyne/gofakes3"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// fixture builds a Backend ready for use by each test.
type fixture struct {
	b         *backend.Backend
	db        *metadata.DB
	dataRoot  string
	projectID int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('s3proj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// Seed the bootstrap super-admin that multipart's initiator FK references.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(id, login, email, password_hash) VALUES (1, 'admin', 'admin@x', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var projectID int64
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='s3proj'`).Scan(&projectID); err != nil {
		t.Fatalf("lookup project: %v", err)
	}
	dataRoot := t.TempDir()
	b := backend.New(dataRoot, db, storage.NewLocks())
	b.DefaultProjectID = projectID
	return &fixture{b: b, db: db, dataRoot: dataRoot, projectID: projectID}
}

// -- Task 1 tests ----------------------------------------------------------

func TestCreateBucket_HappyPath(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("valid-name"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Row exists.
	var n int
	if err := f.db.Reader.QueryRow(`SELECT COUNT(*) FROM s3_buckets WHERE name='valid-name'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 bucket row, got %d", n)
	}
	// Dir exists.
	if _, err := os.Stat(filepath.Join(f.dataRoot, "s3", "valid-name")); err != nil {
		t.Fatalf("dir missing: %v", err)
	}

	// Duplicate → BucketAlreadyExists.
	err := f.b.CreateBucket("valid-name")
	if !gofakes3.HasErrorCode(err, gofakes3.ErrBucketAlreadyExists) {
		t.Fatalf("duplicate: want ErrBucketAlreadyExists, got %v", err)
	}
}

// TestCreateBucket_MkdirFailureCompensatesDBRow pins audit finding #7: when
// the on-disk mkdir fails AFTER the DB insert commits, the backend must
// compensate by soft-deleting the row so the name doesn't stay permanently
// reserved to a bucket with no directory. Without the compensation, a
// failed mkdir left a zombie row blocking all future CreateBucket calls
// with the same name.
func TestCreateBucket_MkdirFailureCompensatesDBRow(t *testing.T) {
	f := newFixture(t)

	// Force mkdir to fail: pre-create dataRoot/s3 as a regular FILE so
	// MkdirAll cannot create a child directory inside it.
	s3Path := filepath.Join(f.dataRoot, "s3")
	if err := os.WriteFile(s3Path, []byte("not a dir"), 0o640); err != nil {
		t.Fatal(err)
	}

	err := f.b.CreateBucket("zombie-name")
	if err == nil {
		t.Fatal("expected mkdir failure, got nil")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Fatalf("expected mkdir error, got: %v", err)
	}

	// Compensation must have flipped deleted_at on the row so the name
	// is free to re-create. Count only LIVE rows.
	var live int
	if err := f.db.Reader.QueryRow(
		`SELECT COUNT(*) FROM s3_buckets WHERE name='zombie-name' AND deleted_at IS NULL`,
	).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("live bucket row still present after mkdir failure: %d", live)
	}

	// Remove the blocker and retry — the name must now be reusable.
	if err := os.Remove(s3Path); err != nil {
		t.Fatal(err)
	}
	if err := f.b.CreateBucket("zombie-name"); err != nil {
		t.Fatalf("recreate after compensation: %v", err)
	}
}

func TestCreateBucket_ReservedAndInvalidNames(t *testing.T) {
	f := newFixture(t)
	cases := []struct{ name, label string }{
		{"s3", "reserved"},
		{"git", "reserved"},
		{"UPPER", "uppercase"},
		{"192.168.1.1", "ipv4"},
		{"ab", "too-short"},
		{"a..b", "double-dot"},
		{"bucket.", "trailing-dot"},
	}
	for _, tc := range cases {
		if err := f.b.CreateBucket(tc.name); err == nil {
			t.Errorf("%s (%s) should fail, got nil", tc.name, tc.label)
		}
	}
}

func TestPutObject_StreamsAndComputesETag(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	// 50 MiB body — confirms streaming (no big buffer).
	size := 50 * 1024 * 1024
	body := bytes.Repeat([]byte{0xab}, size)
	expectedMD5 := md5.Sum(body)
	expectedETag := hex.EncodeToString(expectedMD5[:])

	_, err := f.b.PutObject("bucket1", "big.bin", map[string]string{"Content-Type": "application/octet-stream"},
		bytes.NewReader(body), int64(size), nil)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	// Row + file both exist.
	path := filepath.Join(f.dataRoot, "s3", "bucket1", "big.bin")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if st.Size() != int64(size) {
		t.Fatalf("size=%d want=%d", st.Size(), size)
	}
	obj, err := metadata.NewS3ObjectsRepo(f.db).FindByBucketAndKey(context.Background(), bucketID(t, f, "bucket1"), "big.bin")
	if err != nil {
		t.Fatalf("find row: %v", err)
	}
	if obj.ETag != expectedETag {
		t.Fatalf("etag=%s want=%s", obj.ETag, expectedETag)
	}
	if obj.ContentType != "application/octet-stream" {
		t.Fatalf("ct=%s", obj.ContentType)
	}
}

func TestPutObject_Upsert(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.PutObject("bucket1", "k", nil, bytes.NewReader([]byte("first")), 5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.PutObject("bucket1", "k", nil, bytes.NewReader([]byte("second-body-wins")), 16, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.dataRoot, "s3", "bucket1", "k"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second-body-wins" {
		t.Fatalf("got %q", got)
	}
}

// disconnectReader returns n bytes then errors — simulates mid-stream client drop.
type disconnectReader struct {
	data []byte
	sent int
	stop int
}

func (d *disconnectReader) Read(p []byte) (int, error) {
	if d.sent >= d.stop {
		return 0, io.ErrUnexpectedEOF
	}
	remain := d.stop - d.sent
	if remain > len(p) {
		remain = len(p)
	}
	n := copy(p, d.data[d.sent:d.sent+remain])
	d.sent += n
	return n, nil
}

func TestPutObject_MidStreamDisconnectLeavesNoArtifacts(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte{1}, 5*1024*1024)
	r := &disconnectReader{data: body, stop: 1024 * 1024}
	_, err := f.b.PutObject("bucket1", "partial.bin", nil, r, int64(len(body)), nil)
	if err == nil {
		t.Fatal("expected error on mid-stream disconnect")
	}
	// No file at final path.
	if _, err := os.Stat(filepath.Join(f.dataRoot, "s3", "bucket1", "partial.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file survived: %v", err)
	}
	// No row.
	var n int
	_ = f.db.Reader.QueryRow(`SELECT COUNT(*) FROM s3_objects WHERE key='partial.bin'`).Scan(&n)
	if n != 0 {
		t.Fatalf("unexpected row count %d", n)
	}
}

func TestGetObject_FullAndRange(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	body := []byte("0123456789abcdef")
	if _, err := f.b.PutObject("bucket1", "k", nil, bytes.NewReader(body), int64(len(body)), nil); err != nil {
		t.Fatal(err)
	}
	// Full.
	obj, err := f.b.GetObject("bucket1", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := io.ReadAll(obj.Contents)
	obj.Contents.Close()
	if !bytes.Equal(full, body) {
		t.Fatalf("full mismatch: %q", full)
	}
	if obj.Size != int64(len(body)) {
		t.Fatalf("size %d", obj.Size)
	}
	// Range bytes=4-9.
	rng := &gofakes3.ObjectRangeRequest{Start: 4, End: 9}
	obj, err = f.b.GetObject("bucket1", "k", rng)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(obj.Contents)
	obj.Contents.Close()
	if string(part) != "456789" {
		t.Fatalf("range got %q", part)
	}
}

func TestHeadObject_FastPath(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	body := []byte("hello")
	if _, err := f.b.PutObject("bucket1", "k", map[string]string{"Content-Type": "text/plain"},
		bytes.NewReader(body), int64(len(body)), nil); err != nil {
		t.Fatal(err)
	}
	h, err := f.b.HeadObject("bucket1", "k")
	if err != nil {
		t.Fatal(err)
	}
	if h.Size != int64(len(body)) {
		t.Fatalf("size %d", h.Size)
	}
	// Contents must be a no-op reader — zero bytes.
	buf, _ := io.ReadAll(h.Contents)
	if len(buf) != 0 {
		t.Fatalf("head body not empty: %q", buf)
	}
	h.Contents.Close()
}

func TestDeleteObject_AtomicAndIdempotent(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.PutObject("bucket1", "k", nil, bytes.NewReader([]byte("x")), 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.DeleteObject("bucket1", "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dataRoot, "s3", "bucket1", "k")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
	// Idempotent second delete.
	if _, err := f.b.DeleteObject("bucket1", "k"); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
}

func TestListBucket_1000FilePagination(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bid := bucketID(t, f, "bucket1")
	// Insert 1000 rows directly for speed (bypass PutObject — we're testing listing).
	repo := metadata.NewS3ObjectsRepo(f.db)
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 1000; i++ {
			if _, err := repo.Upsert(ctx, tx, &metadata.S3Object{
				BucketID: bid, Key: fmt.Sprintf("items/%04d.txt", i),
				SizeBytes: int64(i), ETag: "abc", SHA256: "sha256:x",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prefix := &gofakes3.Prefix{HasPrefix: true, Prefix: "items/"}
	totalKeys := 0
	marker := ""
	pages := 0
	for {
		page := gofakes3.ListBucketPage{Marker: marker, MaxKeys: 100}
		res, err := f.b.ListBucket("bucket1", prefix, page)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		totalKeys += len(res.Contents)
		pages++
		if !res.IsTruncated {
			break
		}
		if res.NextMarker == "" {
			t.Fatal("truncated but empty NextMarker")
		}
		marker = res.NextMarker
		if pages > 15 {
			t.Fatalf("pagination loop didn't terminate (>15 pages)")
		}
	}
	if totalKeys != 1000 {
		t.Fatalf("totalKeys=%d want 1000 across %d pages", totalKeys, pages)
	}
	if pages != 10 {
		t.Fatalf("pages=%d want 10", pages)
	}
}

func TestListBucket_DelimiterCollapsesCommonPrefixes(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bid := bucketID(t, f, "bucket1")
	repo := metadata.NewS3ObjectsRepo(f.db)
	keys := []string{"a/x", "a/y", "b/z", "top.txt"}
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := repo.Upsert(ctx, tx, &metadata.S3Object{
				BucketID: bid, Key: k, SizeBytes: 1, ETag: "e", SHA256: "sha256:x",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prefix := &gofakes3.Prefix{HasDelimiter: true, Delimiter: "/"}
	res, err := f.b.ListBucket("bucket1", prefix, gofakes3.ListBucketPage{MaxKeys: 100})
	if err != nil {
		t.Fatal(err)
	}
	// Expect "top.txt" in Contents and "a/" + "b/" in CommonPrefixes.
	if len(res.Contents) != 1 || res.Contents[0].Key != "top.txt" {
		t.Fatalf("contents: %+v", res.Contents)
	}
	gotPrefixes := map[string]bool{}
	for _, p := range res.CommonPrefixes {
		gotPrefixes[p.Prefix] = true
	}
	if !gotPrefixes["a/"] || !gotPrefixes["b/"] {
		t.Fatalf("common prefixes: %+v", res.CommonPrefixes)
	}
}

func TestBucketExists_LockFreeDuringPut(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	// Start a slow PutObject in background.
	body := strings.NewReader(strings.Repeat("x", 8*1024*1024))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = f.b.PutObject("bucket1", "k", nil, body, 8*1024*1024, nil)
	}()
	// Concurrently call BucketExists many times — must never block/deadlock.
	for i := 0; i < 50; i++ {
		ok, err := f.b.BucketExists("bucket1")
		if err != nil || !ok {
			t.Fatalf("BucketExists: %v / %v", ok, err)
		}
	}
	wg.Wait()
}

func TestDeleteBucket_RefusesNonEmpty(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.PutObject("bucket1", "k", nil, bytes.NewReader([]byte("x")), 1, nil); err != nil {
		t.Fatal(err)
	}
	err := f.b.DeleteBucket("bucket1")
	if !gofakes3.HasErrorCode(err, gofakes3.ErrBucketNotEmpty) {
		t.Fatalf("want ErrBucketNotEmpty, got %v", err)
	}
	// After purging the object, delete must succeed.
	if _, err := f.b.DeleteObject("bucket1", "k"); err != nil {
		t.Fatal(err)
	}
	if err := f.b.DeleteBucket("bucket1"); err != nil {
		t.Fatalf("empty delete failed: %v", err)
	}
}

// -- LIFECYCLE-06 lookup-hardening tests -----------------------------------
//
// All seven tests below use a raw UPDATE on `projects.deleted_at` rather than
// going through ProjectsRepo.SoftDelete — the plan 01-01 cascade would mask
// the lookup-hardening behavior we're testing here (the cascade also
// soft-deletes the bucket row, which would trip the `s.deleted_at IS NULL`
// filter that's already in place pre-this-plan). We need to test the new
// `p.deleted_at IS NULL` JOIN filter in isolation.

// rawSoftDeleteProject soft-deletes via raw SQL, decoupled from plan 01-01
// cascade. Used only by lookup-hardening tests in this file.
func rawSoftDeleteProject(t *testing.T, f *fixture, projectID int64) {
	t.Helper()
	if _, err := f.db.Writer.ExecContext(context.Background(),
		`UPDATE projects SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, projectID,
	); err != nil {
		t.Fatalf("raw soft-delete project %d: %v", projectID, err)
	}
}

// TestBackend_FindBucketID_DeletedProject pins LIFECYCLE-06: soft-deleting
// the parent project causes findBucketID (via the public BucketExists
// surface) to return (false, nil) — same shape as a missing bucket.
func TestBackend_FindBucketID_DeletedProject(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket-dp"); err != nil {
		t.Fatal(err)
	}
	// Sanity: live project + live bucket exists.
	ok, err := f.b.BucketExists("bucket-dp")
	if err != nil || !ok {
		t.Fatalf("pre-soft-delete: ok=%v err=%v", ok, err)
	}

	rawSoftDeleteProject(t, f, f.projectID)

	ok, err = f.b.BucketExists("bucket-dp")
	if err != nil {
		t.Fatalf("post-soft-delete err=%v", err)
	}
	if ok {
		t.Fatalf("post-soft-delete BucketExists=true; want false (deleted-project filter not applied)")
	}
}

// TestBackend_FindBucketProjectID_DeletedProject: same setup, exercises
// the FindBucketProjectID public method directly.
func TestBackend_FindBucketProjectID_DeletedProject(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket-fbpid"); err != nil {
		t.Fatal(err)
	}
	pid, ok, err := f.b.FindBucketProjectID(context.Background(), "bucket-fbpid")
	if err != nil || !ok || pid != f.projectID {
		t.Fatalf("pre: pid=%d ok=%v err=%v", pid, ok, err)
	}

	rawSoftDeleteProject(t, f, f.projectID)

	pid, ok, err = f.b.FindBucketProjectID(context.Background(), "bucket-fbpid")
	if err != nil {
		t.Fatalf("post err=%v", err)
	}
	if ok {
		t.Fatalf("post: ok=true pid=%d; want (0, false, nil) (deleted-project filter not applied)", pid)
	}
	if pid != 0 {
		t.Fatalf("post: pid=%d; want 0", pid)
	}
}

// TestBackend_FindBucketID_LiveProjectStillWorks pins regression check —
// the new JOIN must not break the happy path.
func TestBackend_FindBucketID_LiveProjectStillWorks(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket-live"); err != nil {
		t.Fatal(err)
	}
	ok, err := f.b.BucketExists("bucket-live")
	if err != nil || !ok {
		t.Fatalf("BucketExists: ok=%v err=%v", ok, err)
	}
}

// TestBackend_FindBucketID_BucketSoftDeleted_LiveProject pins that the
// existing `s3_buckets.deleted_at IS NULL` filter is preserved — a
// soft-deleted bucket on a LIVE project must still return (false, nil).
func TestBackend_FindBucketID_BucketSoftDeleted_LiveProject(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket-localdel"); err != nil {
		t.Fatal(err)
	}
	// Soft-delete the bucket row only (project stays live).
	if _, err := f.db.Writer.ExecContext(context.Background(),
		`UPDATE s3_buckets SET deleted_at = CURRENT_TIMESTAMP WHERE name = ?`, "bucket-localdel",
	); err != nil {
		t.Fatalf("soft-delete bucket: %v", err)
	}
	ok, err := f.b.BucketExists("bucket-localdel")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ok {
		t.Fatalf("BucketExists=true after soft-delete; want false (s.deleted_at filter regression)")
	}
}

// TestBackend_ListBuckets_DeletedProjectFiltered pins that ListBuckets
// omits buckets whose project is soft-deleted, even when the bucket row
// itself is live (cascade may not have fired yet, or this code is the
// independent gate beyond the cascade).
func TestBackend_ListBuckets_DeletedProjectFiltered(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Bucket on the live default project.
	if err := f.b.CreateBucket("bucket-p1-live"); err != nil {
		t.Fatal(err)
	}
	// Second project + bucket. Soft-delete the second project; its bucket
	// row stays live but must not appear in ListBuckets.
	if _, err := f.db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('s3proj-dead')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var deadPID int64
	if err := f.db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='s3proj-dead'`).Scan(&deadPID); err != nil {
		t.Fatalf("find project: %v", err)
	}
	if err := f.b.CreateBucketForProject("bucket-p2-deadproj", deadPID); err != nil {
		t.Fatalf("create bucket on second project: %v", err)
	}
	rawSoftDeleteProject(t, f, deadPID)

	out, err := f.b.ListBuckets()
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	names := map[string]bool{}
	for _, bi := range out {
		names[bi.Name] = true
	}
	if !names["bucket-p1-live"] {
		t.Fatalf("bucket-p1-live missing from ListBuckets: %+v", names)
	}
	if names["bucket-p2-deadproj"] {
		t.Fatalf("bucket-p2-deadproj leaked into ListBuckets (deleted-project filter not applied): %+v", names)
	}
}

// TestBackend_ListBucketsForProject_DeletedProjectEmpty pins the defensive
// filter on the REST-side ListBucketsForProject — soft-deleted projects
// return empty (not an error).
func TestBackend_ListBucketsForProject_DeletedProjectEmpty(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket-lbfp"); err != nil {
		t.Fatal(err)
	}
	// Sanity.
	pre, err := f.b.ListBucketsForProject(context.Background(), f.projectID)
	if err != nil || len(pre) != 1 {
		t.Fatalf("pre: %d %v", len(pre), err)
	}
	rawSoftDeleteProject(t, f, f.projectID)

	post, err := f.b.ListBucketsForProject(context.Background(), f.projectID)
	if err != nil {
		t.Fatalf("post err: %v", err)
	}
	if len(post) != 0 {
		t.Fatalf("post: %d buckets returned; want 0 (deleted-project filter not applied)", len(post))
	}
}

// TestBackend_GetBucketForProject_DeletedProjectFalse pins the defensive
// filter on GetBucketForProject — soft-deleted projects return (_, false, nil).
func TestBackend_GetBucketForProject_DeletedProjectFalse(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket-gbfp"); err != nil {
		t.Fatal(err)
	}
	bi, ok, err := f.b.GetBucketForProject(context.Background(), f.projectID, "bucket-gbfp")
	if err != nil || !ok || bi.Name != "bucket-gbfp" {
		t.Fatalf("pre: ok=%v name=%q err=%v", ok, bi.Name, err)
	}

	rawSoftDeleteProject(t, f, f.projectID)

	bi, ok, err = f.b.GetBucketForProject(context.Background(), f.projectID, "bucket-gbfp")
	if err != nil {
		t.Fatalf("post err: %v", err)
	}
	if ok {
		t.Fatalf("post: ok=true name=%q; want (_, false, nil)", bi.Name)
	}
}

// -- helpers --------------------------------------------------------------

func bucketID(t *testing.T, f *fixture, name string) int64 {
	t.Helper()
	var id int64
	if err := f.db.Reader.QueryRow(`SELECT id FROM s3_buckets WHERE name=?`, name).Scan(&id); err != nil {
		t.Fatalf("bucketID(%s): %v", name, err)
	}
	return id
}
