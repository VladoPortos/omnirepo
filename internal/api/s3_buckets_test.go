package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	s3backend "github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

func newTestServerWithS3Buckets(t *testing.T) *testServer {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	for _, d := range []string{"certs", "certs/uploaded", "repos", "trash", "tmp", "logs", "s3"} {
		if err := os.MkdirAll(filepath.Join(dataRoot, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	auditLogger, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	holder := omrtls.NewCertHolder()
	certPEM, keyPEM, err := omrtls.GenerateSelfSigned([]string{"localhost"}, time.Hour, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Swap(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatal(err)
	}

	locks := storage.NewLocks()
	be := s3backend.New(dataRoot, db, locks)

	deps := api.Deps{
		DB:            db,
		Users:         metadata.NewUsersRepo(db),
		Sessions:      metadata.NewSessionsRepo(db),
		APIKeys:       metadata.NewAPIKeysRepo(db),
		Projects:      metadata.NewProjectsRepo(db),
		Members:       metadata.NewMembersRepo(db),
		Repos:         metadata.NewReposRepo(db),
		Settings:      metadata.NewSettingsRepo(db),
		UpstreamCreds: metadata.NewUpstreamCredsRepo(db, aead),
		S3Keys:        metadata.NewS3KeysRepo(db),
		S3AEAD:        aead,
		S3Backend:     be,
		S3ObjectsRepo: metadata.NewS3ObjectsRepo(db),
		Holder:        holder,
		DataRoot:      dataRoot,
		Audit:         auditLogger,
		Trash:         storage.NewTrash(filepath.Join(dataRoot, "trash")),
		Locks:         locks,
	}

	mux := chi.NewRouter()
	mux.Get("/healthz", httpx.Healthz())
	mux.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))
	api.Mount(mux, deps)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &testServer{mux: mux, ts: ts, db: db, deps: deps, dataRoot: dataRoot}
}

type s3BucketFixture struct {
	s             *testServer
	aliceCookie   string
	carolCookie   string
	superCookie   string
	projName      string
	projID        int64
	otherProjName string
}

func setupS3BucketFixture(t *testing.T) *s3BucketFixture {
	t.Helper()
	s := newTestServerWithS3Buckets(t)
	ctx := context.Background()
	projectsRepo := metadata.NewProjectsRepo(s.db)
	membersRepo := metadata.NewMembersRepo(s.db)

	aliceID, alicePW := seedTestUser(t, s.db, "alice-b", "a@b", false, false)
	_, carolPW := seedTestUser(t, s.db, "carol-b", "c@b", false, false)
	_, superPW := seedTestUser(t, s.db, "super-b", "s@b", true, false)

	projID, err := projectsRepo.Create(ctx, "bucket-proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := membersRepo.Add(ctx, projID, aliceID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	// A second project alice is NOT a member of.
	if _, err := projectsRepo.Create(ctx, "other-proj", ""); err != nil {
		t.Fatal(err)
	}

	aliceCookie, _, code := s.login(t, "alice-b", alicePW)
	if code != 200 {
		t.Fatalf("alice login code=%d", code)
	}
	carolCookie, _, code := s.login(t, "carol-b", carolPW)
	if code != 200 {
		t.Fatalf("carol login code=%d", code)
	}
	superCookie, _, code := s.login(t, "super-b", superPW)
	if code != 200 {
		t.Fatalf("super login code=%d", code)
	}
	return &s3BucketFixture{
		s:             s,
		aliceCookie:   aliceCookie,
		carolCookie:   carolCookie,
		superCookie:   superCookie,
		projName:      "bucket-proj",
		projID:        projID,
		otherProjName: "other-proj",
	}
}

func TestS3Buckets_CreateList_HappyPath(t *testing.T) {
	f := setupS3BucketFixture(t)

	// Create.
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.aliceCookie,
		map[string]any{"name": "walkthrough-bkt"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	var item map[string]any
	if err := json.Unmarshal(buf, &item); err != nil {
		t.Fatal(err)
	}
	if item["name"] != "walkthrough-bkt" {
		t.Fatalf("create name mismatch: %s", buf)
	}

	// On-disk directory must exist.
	if _, err := os.Stat(filepath.Join(f.s.dataRoot, "s3", "walkthrough-bkt")); err != nil {
		t.Fatalf("bucket dir not created: %v", err)
	}

	// DB row must be linked to this project.
	var gotProjID int64
	if err := f.s.db.Reader.QueryRowContext(context.Background(),
		`SELECT project_id FROM s3_buckets WHERE name=? AND deleted_at IS NULL`,
		"walkthrough-bkt").Scan(&gotProjID); err != nil {
		t.Fatalf("select bucket: %v", err)
	}
	if gotProjID != f.projID {
		t.Fatalf("bucket bound to project %d, want %d", gotProjID, f.projID)
	}

	// List.
	resp, buf = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d", resp.StatusCode)
	}
	var listed []map[string]any
	if err := json.Unmarshal(buf, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("list count=%d want 1", len(listed))
	}
	if listed[0]["name"] != "walkthrough-bkt" {
		t.Fatalf("list name mismatch: %s", buf)
	}
}

func TestS3Buckets_DuplicateName_Returns409(t *testing.T) {
	f := setupS3BucketFixture(t)
	body := map[string]any{"name": "dup-bkt"}
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/", f.aliceCookie, body)
	if resp.StatusCode != 201 {
		t.Fatalf("first create code=%d", resp.StatusCode)
	}
	resp, _ = f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/", f.aliceCookie, body)
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate create code=%d, want 409", resp.StatusCode)
	}
}

func TestS3Buckets_InvalidName_Returns422(t *testing.T) {
	f := setupS3BucketFixture(t)
	for _, bad := range []string{"AB", "a", "has spaces", "BadCase", "..", "name.ending."} {
		resp, buf := f.s.doBytes(t, "POST",
			"/api/v1/projects/"+f.projName+"/s3-buckets/",
			f.aliceCookie,
			map[string]any{"name": bad})
		if resp.StatusCode != 422 {
			t.Fatalf("invalid name %q code=%d body=%s, want 422", bad, resp.StatusCode, buf)
		}
	}
}

func TestS3Buckets_EmptyName_Returns422(t *testing.T) {
	f := setupS3BucketFixture(t)
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.aliceCookie,
		map[string]any{"name": "   "})
	if resp.StatusCode != 422 {
		t.Fatalf("empty name code=%d, want 422", resp.StatusCode)
	}
}

func TestS3Buckets_NonMember_Returns403(t *testing.T) {
	f := setupS3BucketFixture(t)
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.carolCookie,
		map[string]any{"name": "forbidden-bkt"})
	if resp.StatusCode != 403 {
		t.Fatalf("non-member POST code=%d, want 403", resp.StatusCode)
	}
	resp, _ = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.carolCookie, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-member GET code=%d, want 403", resp.StatusCode)
	}
}

func TestS3Buckets_SuperAdmin_AlwaysSucceeds(t *testing.T) {
	f := setupS3BucketFixture(t)
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.superCookie,
		map[string]any{"name": "super-bkt"})
	if resp.StatusCode != 201 {
		t.Fatalf("super-admin POST code=%d body=%s", resp.StatusCode, buf)
	}
}

func TestS3Buckets_UnknownProject_Returns404(t *testing.T) {
	f := setupS3BucketFixture(t)
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/does-not-exist/s3-buckets/",
		f.aliceCookie,
		map[string]any{"name": "ghost"})
	if resp.StatusCode != 404 {
		t.Fatalf("unknown project code=%d, want 404", resp.StatusCode)
	}
}

func TestS3Buckets_List_ScopedToProject(t *testing.T) {
	f := setupS3BucketFixture(t)
	// super-admin creates one bucket under each project.
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.superCookie,
		map[string]any{"name": "proj-a-bkt"})
	if resp.StatusCode != 201 {
		t.Fatalf("create proj A code=%d", resp.StatusCode)
	}
	resp, _ = f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.otherProjName+"/s3-buckets/",
		f.superCookie,
		map[string]any{"name": "proj-b-bkt"})
	if resp.StatusCode != 201 {
		t.Fatalf("create proj B code=%d", resp.StatusCode)
	}
	// List on project A should return only proj-a-bkt.
	resp, buf := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d", resp.StatusCode)
	}
	var listed []map[string]any
	if err := json.Unmarshal(buf, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0]["name"] != "proj-a-bkt" {
		t.Fatalf("list not scoped to project A: %s", buf)
	}
}

func TestS3Buckets_AuditCreate(t *testing.T) {
	f := setupS3BucketFixture(t)
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.aliceCookie,
		map[string]any{"name": "audited-bkt"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d", resp.StatusCode)
	}
	var count int
	if err := f.s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM audit_log WHERE event_kind=? AND target_id=?`,
		string(audit.EvtS3BucketCreated), "audited-bkt").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit count=%d, want 1", count)
	}
}

// seedBucketObject inserts one s3_objects row directly — the REST layer
// can't PUT objects (SigV4 is protocol-only), so tests that care about
// object rows populate them via the repo. The on-disk file is not needed
// for the list endpoint; it reads from the DB only.
func seedBucketObject(t *testing.T, f *s3BucketFixture, bucketName, key string, size int64) {
	t.Helper()
	ctx := context.Background()
	var bucketID int64
	if err := f.s.db.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_buckets WHERE name=? AND deleted_at IS NULL`,
		bucketName).Scan(&bucketID); err != nil {
		t.Fatalf("lookup bucket %q: %v", bucketName, err)
	}
	_, err := f.s.db.Writer.ExecContext(ctx,
		`INSERT INTO s3_objects(bucket_id, key, size_bytes, etag, content_type, metadata_json, sha256)
		 VALUES (?, ?, ?, ?, 'application/octet-stream', '{}', '')`,
		bucketID, key, size, fmt.Sprintf("etag-%s", key))
	if err != nil {
		t.Fatalf("seed object %q: %v", key, err)
	}
}

func createBucketAsAlice(t *testing.T, f *s3BucketFixture, name string) {
	t.Helper()
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.aliceCookie, map[string]any{"name": name})
	if resp.StatusCode != 201 {
		t.Fatalf("create %q code=%d body=%s", name, resp.StatusCode, buf)
	}
}

func TestS3Buckets_List_IncludesSizeAndCount(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "size-bkt")
	seedBucketObject(t, f, "size-bkt", "a.txt", 100)
	seedBucketObject(t, f, "size-bkt", "b.txt", 250)

	resp, buf := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d", resp.StatusCode)
	}
	var items []map[string]any
	_ = json.Unmarshal(buf, &items)
	if len(items) != 1 {
		t.Fatalf("want 1 bucket, got %d", len(items))
	}
	if int64(items[0]["size_bytes"].(float64)) != 350 {
		t.Fatalf("size_bytes=%v want 350", items[0]["size_bytes"])
	}
	if int64(items[0]["object_count"].(float64)) != 2 {
		t.Fatalf("object_count=%v want 2", items[0]["object_count"])
	}
}

func TestS3Buckets_GetBucket_HappyPath(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "detail-bkt")
	seedBucketObject(t, f, "detail-bkt", "one.bin", 42)

	resp, buf := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/detail-bkt",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get code=%d body=%s", resp.StatusCode, buf)
	}
	var item map[string]any
	_ = json.Unmarshal(buf, &item)
	if item["name"] != "detail-bkt" {
		t.Fatalf("name=%v want detail-bkt", item["name"])
	}
	if int64(item["size_bytes"].(float64)) != 42 {
		t.Fatalf("size_bytes=%v", item["size_bytes"])
	}
}

func TestS3Buckets_GetBucket_OtherProject_Returns404(t *testing.T) {
	f := setupS3BucketFixture(t)
	// super-admin creates a bucket in the OTHER project alice is not a member of.
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.otherProjName+"/s3-buckets/",
		f.superCookie, map[string]any{"name": "other-bkt"})
	if resp.StatusCode != 201 {
		t.Fatalf("setup create code=%d", resp.StatusCode)
	}
	// alice (member of projName only) asks for it under HER project's URL
	// — must 404 (not leak existence across projects).
	resp, _ = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/other-bkt",
		f.aliceCookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-project GET code=%d want 404", resp.StatusCode)
	}
}

func TestS3Buckets_DeleteBucket_HappyPath(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "delete-me")

	resp, _ := f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/s3-buckets/delete-me",
		f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete code=%d", resp.StatusCode)
	}
	// Idempotency check — second DELETE must 404.
	resp, _ = f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/s3-buckets/delete-me",
		f.aliceCookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("second delete code=%d want 404", resp.StatusCode)
	}
	// Audit event written.
	var count int
	_ = f.s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM audit_log WHERE event_kind=? AND target_id=?`,
		string(audit.EvtS3BucketDeleted), "delete-me").Scan(&count)
	if count != 1 {
		t.Fatalf("delete audit count=%d want 1", count)
	}
}

func TestS3Buckets_DeleteBucket_NonEmpty_Returns409(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "not-empty")
	seedBucketObject(t, f, "not-empty", "keep.txt", 7)

	resp, _ := f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/s3-buckets/not-empty",
		f.aliceCookie, nil)
	if resp.StatusCode != 409 {
		t.Fatalf("delete-non-empty code=%d want 409", resp.StatusCode)
	}
}

func TestS3Buckets_DeleteBucket_NonMember_Returns403(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "carol-cant-touch")
	resp, _ := f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/s3-buckets/carol-cant-touch",
		f.carolCookie, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-member delete code=%d want 403", resp.StatusCode)
	}
}

func TestS3Buckets_ListObjects_HappyPath(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "objs")
	seedBucketObject(t, f, "objs", "a.txt", 11)
	seedBucketObject(t, f, "objs", "b.txt", 22)
	seedBucketObject(t, f, "objs", "img/1.png", 33)

	resp, buf := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/objs/objects",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list objects code=%d body=%s", resp.StatusCode, buf)
	}
	var page map[string]any
	_ = json.Unmarshal(buf, &page)
	items, _ := page["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("want 3 objects, got %d: %s", len(items), buf)
	}
	if page["truncated"].(bool) {
		t.Fatalf("want truncated=false")
	}
}

func TestS3Buckets_ListObjects_PrefixFilter(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "prefixed")
	seedBucketObject(t, f, "prefixed", "docs/a.md", 10)
	seedBucketObject(t, f, "prefixed", "docs/b.md", 20)
	seedBucketObject(t, f, "prefixed", "img/c.png", 30)

	resp, buf := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/prefixed/objects?prefix=docs/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("prefix list code=%d", resp.StatusCode)
	}
	var page map[string]any
	_ = json.Unmarshal(buf, &page)
	items, _ := page["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("prefix filter: want 2 got %d: %s", len(items), buf)
	}
}

func TestS3Buckets_ListObjects_Pagination(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "paged")
	for i := 0; i < 5; i++ {
		seedBucketObject(t, f, "paged", fmt.Sprintf("k-%02d", i), int64(i+1))
	}
	resp, buf := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/paged/objects?limit=2",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("page1 code=%d", resp.StatusCode)
	}
	var p1 map[string]any
	_ = json.Unmarshal(buf, &p1)
	items1, _ := p1["items"].([]any)
	if len(items1) != 2 {
		t.Fatalf("page1 len=%d want 2", len(items1))
	}
	if !p1["truncated"].(bool) {
		t.Fatalf("page1 expected truncated")
	}
	marker := p1["next_marker"].(string)
	if marker == "" {
		t.Fatal("page1 next_marker empty")
	}
	resp, buf = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/paged/objects?limit=2&marker="+marker,
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("page2 code=%d", resp.StatusCode)
	}
	var p2 map[string]any
	_ = json.Unmarshal(buf, &p2)
	items2, _ := p2["items"].([]any)
	if len(items2) != 2 {
		t.Fatalf("page2 len=%d want 2", len(items2))
	}
}

func TestS3Buckets_ListObjects_NonMember_Returns403(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "private-bkt")
	resp, _ := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-buckets/private-bkt/objects",
		f.carolCookie, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-member objects code=%d want 403", resp.StatusCode)
	}
}

func TestProjectDetail_IncludesBuckets(t *testing.T) {
	f := setupS3BucketFixture(t)
	createBucketAsAlice(t, f, "detail-a")
	createBucketAsAlice(t, f, "detail-b")
	seedBucketObject(t, f, "detail-a", "obj.bin", 999)

	resp, buf := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName,
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("project detail code=%d", resp.StatusCode)
	}
	var detail map[string]any
	_ = json.Unmarshal(buf, &detail)
	buckets, _ := detail["buckets"].([]any)
	if len(buckets) != 2 {
		t.Fatalf("want 2 buckets in detail, got %d: %s", len(buckets), buf)
	}
	found := false
	for _, b := range buckets {
		bm := b.(map[string]any)
		if bm["name"] == "detail-a" {
			found = true
			if int64(bm["size_bytes"].(float64)) != 999 {
				t.Fatalf("detail-a size=%v want 999", bm["size_bytes"])
			}
		}
	}
	if !found {
		t.Fatalf("detail-a not in buckets: %s", buf)
	}
}
