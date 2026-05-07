package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// newTestServerWithS3Keys wires the S3Keys + AEAD deps into the test server.
func newTestServerWithS3Keys(t *testing.T) (*testServer, *omrcrypto.AEAD) {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	for _, d := range []string{"certs", "certs/uploaded", "repos", "trash", "tmp", "logs"} {
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
		Holder:        holder,
		DataRoot:      dataRoot,
		Audit:         auditLogger,
		Trash:         storage.NewTrash(filepath.Join(dataRoot, "trash")),
		Locks:         storage.NewLocks(),
	}

	mux := chi.NewRouter()
	mux.Get("/healthz", httpx.Healthz())
	mux.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))
	api.Mount(mux, deps)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &testServer{mux: mux, ts: ts, db: db, deps: deps, dataRoot: dataRoot}, aead
}

type s3KeyFixture struct {
	s           *testServer
	aead        *omrcrypto.AEAD
	aliceCookie string
	carolCookie string
	superCookie string
	projName    string
	projID      int64
}

func setupS3KeyFixture(t *testing.T) *s3KeyFixture {
	t.Helper()
	s, aead := newTestServerWithS3Keys(t)
	ctx := context.Background()
	projectsRepo := metadata.NewProjectsRepo(s.db)
	membersRepo := metadata.NewMembersRepo(s.db)

	aliceID, alicePW := seedTestUser(t, s.db, "alice-s3", "a@s3", false, false)
	_, carolPW := seedTestUser(t, s.db, "carol-s3", "c@s3", false, false)
	_, superPW := seedTestUser(t, s.db, "super-s3", "s@s3", true, false)

	projID, err := projectsRepo.Create(ctx, "s3proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := membersRepo.Add(ctx, projID, aliceID, "maintainer"); err != nil {
		t.Fatal(err)
	}

	aliceCookie, _, code := s.login(t, "alice-s3", alicePW)
	if code != 200 {
		t.Fatalf("alice login code=%d", code)
	}
	carolCookie, _, code := s.login(t, "carol-s3", carolPW)
	if code != 200 {
		t.Fatalf("carol login code=%d", code)
	}
	superCookie, _, code := s.login(t, "super-s3", superPW)
	if code != 200 {
		t.Fatalf("super login code=%d", code)
	}

	return &s3KeyFixture{
		s:           s,
		aead:        aead,
		aliceCookie: aliceCookie,
		carolCookie: carolCookie,
		superCookie: superCookie,
		projName:    "s3proj",
		projID:      projID,
	}
}

func TestS3Keys_CreateListRevoke_HappyPath(t *testing.T) {
	f := setupS3KeyFixture(t)

	// Create
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie,
		map[string]any{"label": "ci-prod"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatal(err)
	}
	// Shown-once: secret field must be present in create response.
	secret, ok := created["secret"].(string)
	if !ok || secret == "" {
		t.Fatalf("create response missing secret: %s", buf)
	}
	akid, ok := created["access_key_id"].(string)
	if !ok || !strings.HasPrefix(akid, "AKIA") {
		t.Fatalf("create response bad access_key_id: %s", buf)
	}
	id := int64(created["id"].(float64))
	if id == 0 {
		t.Fatal("no id in response")
	}
	if created["label"] != "ci-prod" {
		t.Fatalf("label mismatch: %s", buf)
	}

	// List — secret MUST NOT appear.
	resp, buf = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d", resp.StatusCode)
	}
	if strings.Contains(string(buf), secret) {
		t.Fatalf("list response contains plaintext secret!")
	}
	var listed []map[string]any
	if err := json.Unmarshal(buf, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("list count=%d, want 1", len(listed))
	}
	if _, has := listed[0]["secret"]; has {
		t.Fatal("list item has secret field")
	}

	// Revoke
	resp, _ = f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/"+itoa(id),
		f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("revoke code=%d", resp.StatusCode)
	}

	// List after revoke — should be empty.
	resp, buf = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list-after-revoke code=%d", resp.StatusCode)
	}
	if err := json.Unmarshal(buf, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("list-after-revoke count=%d, want 0", len(listed))
	}
}

func TestS3Keys_NonMember_Returns403(t *testing.T) {
	f := setupS3KeyFixture(t)
	// carol is not a member of s3proj.
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.carolCookie,
		map[string]any{"label": "x"})
	if resp.StatusCode != 403 {
		t.Fatalf("non-member POST code=%d, want 403", resp.StatusCode)
	}
}

func TestS3Keys_SuperAdmin_AlwaysSucceeds(t *testing.T) {
	f := setupS3KeyFixture(t)
	// super-admin is not a member but should bypass.
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.superCookie,
		map[string]any{"label": "admin-key"})
	if resp.StatusCode != 201 {
		t.Fatalf("super-admin POST code=%d body=%s", resp.StatusCode, buf)
	}
}

func TestS3Keys_EmptyLabel_Returns422(t *testing.T) {
	f := setupS3KeyFixture(t)
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie,
		map[string]any{"label": ""})
	if resp.StatusCode != 422 {
		t.Fatalf("empty label code=%d, want 422", resp.StatusCode)
	}
	// Whitespace-only label.
	resp, _ = f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie,
		map[string]any{"label": "   "})
	if resp.StatusCode != 422 {
		t.Fatalf("whitespace label code=%d, want 422", resp.StatusCode)
	}
}

func TestS3Keys_AuditCreate_NoSecret(t *testing.T) {
	f := setupS3KeyFixture(t)

	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie,
		map[string]any{"label": "audit-test"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	secret := created["secret"].(string)

	// Check audit_log for the create event.
	var detailsJSON string
	err := f.s.db.Reader.QueryRowContext(context.Background(),
		`SELECT details_json FROM audit_log WHERE event_kind = ?`,
		string(audit.EvtS3AccessKeyCreated)).Scan(&detailsJSON)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if strings.Contains(detailsJSON, secret) {
		t.Fatalf("audit details_json contains plaintext secret: %s", detailsJSON)
	}
	if !strings.Contains(detailsJSON, "access_key_id") {
		t.Fatalf("audit details_json missing access_key_id: %s", detailsJSON)
	}
}

func TestS3Keys_AuditRevoke(t *testing.T) {
	f := setupS3KeyFixture(t)

	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie,
		map[string]any{"label": "revoke-audit"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d", resp.StatusCode)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	id := int64(created["id"].(float64))

	resp, _ = f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/"+itoa(id),
		f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("revoke code=%d", resp.StatusCode)
	}

	var count int
	err := f.s.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM audit_log WHERE event_kind = ?`,
		string(audit.EvtS3AccessKeyRevoked)).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatal("no revoke audit event found")
	}
}

func TestS3Keys_Create_EncryptsSecret(t *testing.T) {
	f := setupS3KeyFixture(t)

	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie,
		map[string]any{"label": "enc-test"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d", resp.StatusCode)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	secret := created["secret"].(string)
	akid := created["access_key_id"].(string)

	// Read the raw DB row and confirm secret_enc does NOT contain the plaintext.
	var secretEnc []byte
	err := f.s.db.Reader.QueryRowContext(context.Background(),
		`SELECT secret_enc FROM s3_access_keys WHERE access_key_id = ?`, akid).Scan(&secretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(secretEnc), secret) {
		t.Fatal("secret_enc contains plaintext secret")
	}
	// Verify AEAD can round-trip the stored value.
	plaintext, err := f.aead.Decrypt(string(secretEnc))
	if err != nil {
		t.Fatalf("AEAD.Decrypt: %v", err)
	}
	if string(plaintext) != secret {
		t.Fatalf("AEAD round-trip: got %q, want %q", plaintext, secret)
	}
}

func TestS3Keys_Create_RevokedAtSetOnRevoke(t *testing.T) {
	f := setupS3KeyFixture(t)

	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.aliceCookie,
		map[string]any{"label": "revoke-check"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d", resp.StatusCode)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	id := int64(created["id"].(float64))

	resp, _ = f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/"+itoa(id),
		f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("revoke code=%d", resp.StatusCode)
	}

	var revokedAt sql.NullString
	err := f.s.db.Reader.QueryRowContext(context.Background(),
		`SELECT revoked_at FROM s3_access_keys WHERE id = ?`, id).Scan(&revokedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Valid || revokedAt.String == "" {
		t.Fatal("revoked_at not set after revoke")
	}
}

func TestS3Keys_Unauthenticated_Returns401(t *testing.T) {
	f := setupS3KeyFixture(t)
	resp, _ := f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		"", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated code=%d, want 401", resp.StatusCode)
	}
}
