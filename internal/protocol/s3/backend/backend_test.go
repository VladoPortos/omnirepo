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

// -- helpers --------------------------------------------------------------

func bucketID(t *testing.T, f *fixture, name string) int64 {
	t.Helper()
	var id int64
	if err := f.db.Reader.QueryRow(`SELECT id FROM s3_buckets WHERE name=?`, name).Scan(&id); err != nil {
		t.Fatalf("bucketID(%s): %v", name, err)
	}
	return id
}
