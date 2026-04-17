package api_test

import (
	"context"
	"encoding/json"
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
	s              *testServer
	aliceCookie    string
	carolCookie    string
	superCookie    string
	projName       string
	projID         int64
	otherProjName  string
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
	if err := membersRepo.Add(ctx, projID, aliceID); err != nil {
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
