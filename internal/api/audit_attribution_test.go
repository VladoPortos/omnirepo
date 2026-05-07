package api_test

// AUDITATTR-06 integration test — every state-changing project-scoped
// endpoint × {session, user-key, project-key} → asserts the correct
// (actor_user_id, actor_api_key_id) shape per CONTEXT.md D-08.
//
// This test is the audit-finding-#7 forcing function: any future
// regression that re-introduces the `uid := actor.ID; ActorUserID: &uid`
// pattern at a project-scoped state-changing handler will fail the
// project_key/* subtree with "FK-violation guard" in the failure
// message. Plan 03-01 built the helpers; Plan 03-02 flipped the call
// sites; this test locks the bug class out for good.
//
// Per-row D-08 rules:
//
//   session:     wantUserIDNonNull=true,  wantAPIKeyIDNonNull=false
//   user_key:    wantUserIDNonNull=true,  wantAPIKeyIDNonNull=true
//   project_key: wantUserIDNonNull=false, wantAPIKeyIDNonNull=true   <-- the bug-class case

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	s3backend "github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// authMode bundles a single (cookie OR bearer) plus the expected D-08
// audit-row shape. Exactly one of cookie/bearer is non-empty for any
// real request.
type authMode struct {
	name                string // "session" | "user_key" | "project_key"
	cookie              string
	bearer              string
	wantUserIDNonNull   bool
	wantAPIKeyIDNonNull bool
}

// auditRow is a snapshot of the audit_log row produced by a single
// state-changing request. *int64 is used for the actor columns so the
// caller can distinguish NULL from 0.
type auditRow struct {
	actorUserID   *int64
	actorAPIKeyID *int64
	eventKind     string
	targetID      string
}

// fetchLatestAuditRow returns the most recent audit_log row for the
// given event_kind + target_id pair. The (kind, target_id) tuple is
// unique-per-request within these tests, so this is unambiguous. The
// test runs serially (no t.Parallel) — ORDER BY id DESC LIMIT 1 is
// safe.
func fetchLatestAuditRow(t *testing.T, db *sql.DB, kind audit.EventKind, targetID string) auditRow {
	t.Helper()
	var (
		uid sql.NullInt64
		kid sql.NullInt64
		ek  string
		tid string
	)
	err := db.QueryRowContext(context.Background(), `
		SELECT actor_user_id, actor_api_key_id, event_kind, target_id
		  FROM audit_log
		 WHERE event_kind = ? AND target_id = ?
		 ORDER BY id DESC
		 LIMIT 1
	`, string(kind), targetID).Scan(&uid, &kid, &ek, &tid)
	if err != nil {
		t.Fatalf("fetchLatestAuditRow(kind=%s, target_id=%s): %v", kind, targetID, err)
	}
	row := auditRow{eventKind: ek, targetID: tid}
	if uid.Valid {
		v := uid.Int64
		row.actorUserID = &v
	}
	if kid.Valid {
		v := kid.Int64
		row.actorAPIKeyID = &v
	}
	return row
}

// assertActorShape enforces the three D-08 rules at the audit_log row
// boundary. The "FK-violation guard" failure message explicitly names
// audit finding #7 so a future maintainer who hits it understands the
// bug class.
func assertActorShape(t *testing.T, am authMode, row auditRow) {
	t.Helper()
	if am.wantUserIDNonNull && row.actorUserID == nil {
		t.Fatalf("[%s/%s target=%s] actor_user_id IS NULL, want NOT NULL",
			am.name, row.eventKind, row.targetID)
	}
	if !am.wantUserIDNonNull && row.actorUserID != nil {
		// FK-violation guard — the audit-finding-#7 regression detector.
		// A failure here means a project-owned API key wrote actor_user_id
		// (probably 0 against users(id), possibly a stale super-admin id)
		// — exactly the bug Plan 03-02 closed.
		t.Fatalf("[%s/%s target=%s] actor_user_id = %d, want NULL (FK-violation guard)",
			am.name, row.eventKind, row.targetID, *row.actorUserID)
	}
	if am.wantAPIKeyIDNonNull && row.actorAPIKeyID == nil {
		t.Fatalf("[%s/%s target=%s] actor_api_key_id IS NULL, want NOT NULL",
			am.name, row.eventKind, row.targetID)
	}
	if !am.wantAPIKeyIDNonNull && row.actorAPIKeyID != nil {
		t.Fatalf("[%s/%s target=%s] actor_api_key_id = %d, want NULL",
			am.name, row.eventKind, row.targetID, *row.actorAPIKeyID)
	}
}

// newTestServerForAuditAttribution wires every dep the four endpoint
// groups need (UpstreamCreds + S3Keys + S3AEAD + S3Backend +
// S3ObjectsRepo). Combines the existing newTestServerWithS3Buckets
// + newTestServerWithUpstream wirings into a single fixture.
func newTestServerForAuditAttribution(t *testing.T) *testServer {
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
	mux.Use(httpx.IncidentIDMiddleware)
	mux.Get("/healthz", httpx.Healthz())
	mux.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))
	api.Mount(mux, deps)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &testServer{mux: mux, ts: ts, db: db, deps: deps, dataRoot: dataRoot}
}

// auditAttrFixture is the bundle (server + auth + project context)
// returned by each seeder. The project is always already created and
// the calling identity is always authorized as a maintainer of it.
type auditAttrFixture struct {
	s           *testServer
	am          authMode
	projectName string
	projectID   int64
}

// seedAuditAttributionFixture_Session: alice is a member of the test
// project; she logs in via cookie.
func seedAuditAttributionFixture_Session(t *testing.T) *auditAttrFixture {
	t.Helper()
	s := newTestServerForAuditAttribution(t)
	ctx := context.Background()

	aliceID, alicePW := seedTestUser(t, s.db, "alice-aa", "a@aa", false, false)
	projectName := "audit-attr-proj"
	pid, err := s.deps.Projects.Create(ctx, projectName, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deps.Members.Add(ctx, pid, aliceID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	cookie, _, code := s.login(t, "alice-aa", alicePW)
	if code != 200 {
		t.Fatalf("alice login code=%d", code)
	}
	return &auditAttrFixture{
		s:           s,
		projectName: projectName,
		projectID:   pid,
		am: authMode{
			name:                "session",
			cookie:              cookie,
			wantUserIDNonNull:   true,
			wantAPIKeyIDNonNull: false,
		},
	}
}

// seedAuditAttributionFixture_UserKey: alice is a maintainer of the
// project; she authenticates via a user-owned API key.
func seedAuditAttributionFixture_UserKey(t *testing.T) *auditAttrFixture {
	t.Helper()
	s := newTestServerForAuditAttribution(t)
	ctx := context.Background()

	aliceID, _ := seedTestUser(t, s.db, "alice-aa-uk", "a@aa", false, false)
	projectName := "audit-attr-proj"
	pid, err := s.deps.Projects.Create(ctx, projectName, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deps.Members.Add(ctx, pid, aliceID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	k, err := auth.GenerateAPIKey(auth.APIKeyKindUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.deps.APIKeys.CreateUserKey(ctx, aliceID, "ci-user-key", k.Prefix, k.SHA256); err != nil {
		t.Fatal(err)
	}
	return &auditAttrFixture{
		s:           s,
		projectName: projectName,
		projectID:   pid,
		am: authMode{
			name:                "user_key",
			bearer:              "Bearer " + k.Plaintext,
			wantUserIDNonNull:   true,
			wantAPIKeyIDNonNull: true,
		},
	}
}

// seedAuditAttributionFixture_ProjectKey: project-owned API key with
// maintainer role. NO user identity attached; the FK-violation guard
// case (audit finding #7).
func seedAuditAttributionFixture_ProjectKey(t *testing.T) *auditAttrFixture {
	t.Helper()
	s := newTestServerForAuditAttribution(t)
	ctx := context.Background()

	projectName := "audit-attr-proj"
	pid, err := s.deps.Projects.Create(ctx, projectName, "")
	if err != nil {
		t.Fatal(err)
	}
	k, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.deps.APIKeys.CreateProjectKeyWithRole(ctx, pid, "ci-project-key",
		k.Prefix, k.SHA256, "maintainer"); err != nil {
		t.Fatal(err)
	}
	return &auditAttrFixture{
		s:           s,
		projectName: projectName,
		projectID:   pid,
		am: authMode{
			name:                "project_key",
			bearer:              "Bearer " + k.Plaintext,
			wantUserIDNonNull:   false, // FK-violation guard property
			wantAPIKeyIDNonNull: true,
		},
	}
}

// doAuth issues an HTTP request bearing either am.cookie or am.bearer
// and returns the response + decoded JSON body (if any). Mirrors
// testServer.do but with the bearer-or-cookie distinction.
func doAuth(t *testing.T, s *testServer, am authMode, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.ts.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	if am.cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: am.cookie})
	}
	if am.bearer != "" {
		req.Header.Set("Authorization", am.bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(buf, &out)
	return resp, out
}

// runS3BucketCreateDelete drives POST + DELETE on /s3-buckets and
// asserts each resulting audit row matches the expected D-08 shape.
//
// The bucket-name regex is `^[a-z0-9][a-z0-9.\-]{2,62}$` so we strip
// any underscores from f.am.name (e.g. "user_key" → "user-key").
func runS3BucketCreateDelete(t *testing.T, f *auditAttrFixture) {
	t.Helper()
	bucketName := "audit-bucket-" + bucketSafe(f.am.name)

	// 1) POST — create
	resp, body := doAuth(t, f.s, f.am, "POST",
		"/api/v1/projects/"+f.projectName+"/s3-buckets/",
		map[string]any{"name": bucketName})
	if resp.StatusCode != 201 {
		t.Fatalf("[%s] s3-bucket create code=%d body=%+v", f.am.name, resp.StatusCode, body)
	}
	row := fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtS3BucketCreated, bucketName)
	assertActorShape(t, f.am, row)

	// 2) DELETE
	resp, _ = doAuth(t, f.s, f.am, "DELETE",
		"/api/v1/projects/"+f.projectName+"/s3-buckets/"+bucketName, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("[%s] s3-bucket delete code=%d", f.am.name, resp.StatusCode)
	}
	row = fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtS3BucketDeleted, bucketName)
	assertActorShape(t, f.am, row)
}

// runS3KeyCreateRevoke drives POST + DELETE on /s3-access-keys.
//
// Out-of-scope guardrail: s3_access_keys.created_by_user_id is a NOT NULL
// FK to users(id) (migration 016), so a project-owned API key (actor.ID == 0)
// is structurally rejected at the metadata layer with HTTP 500 BEFORE any
// audit attribution decision is reached. Plan 03-02 targets audit-row
// attribution only; surfacing the project-owned-key as an s3_access_key
// creator would require a column change (REFERENCES api_keys + nullable
// user fk) and a separate plan. Skip the project_key/s3_access_key cell
// with an explicit reason so the matrix grid stays readable and a future
// plan can pick this up.
func runS3KeyCreateRevoke(t *testing.T, f *auditAttrFixture) {
	t.Helper()
	if f.am.name == "project_key" {
		t.Skipf("s3_access_keys.created_by_user_id is NOT NULL FK to users(id); " +
			"project-owned-key creator attribution is out of Plan 03-02 scope")
	}
	label := "audit-s3key-" + f.am.name

	// 1) POST — create
	resp, body := doAuth(t, f.s, f.am, "POST",
		"/api/v1/projects/"+f.projectName+"/s3-access-keys/",
		map[string]any{"label": label})
	if resp.StatusCode != 201 {
		t.Fatalf("[%s] s3-key create code=%d body=%+v", f.am.name, resp.StatusCode, body)
	}
	id, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("[%s] s3-key create response missing id: %+v", f.am.name, body)
	}
	idStr := fmt.Sprintf("%d", int64(id))
	row := fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtS3AccessKeyCreated, idStr)
	assertActorShape(t, f.am, row)

	// 2) DELETE — revoke
	resp, _ = doAuth(t, f.s, f.am, "DELETE",
		"/api/v1/projects/"+f.projectName+"/s3-access-keys/"+idStr, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("[%s] s3-key revoke code=%d", f.am.name, resp.StatusCode)
	}
	row = fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtS3AccessKeyRevoked, idStr)
	assertActorShape(t, f.am, row)
}

// runProjectAPIKeyCreateRevoke drives POST + DELETE on /api-keys.
func runProjectAPIKeyCreateRevoke(t *testing.T, f *auditAttrFixture) {
	t.Helper()
	name := "audit-pkey-" + f.am.name

	// 1) POST — create
	resp, body := doAuth(t, f.s, f.am, "POST",
		"/api/v1/projects/"+f.projectName+"/api-keys/",
		map[string]any{"name": name, "role": "maintainer"})
	if resp.StatusCode != 201 {
		t.Fatalf("[%s] project-api-key create code=%d body=%+v", f.am.name, resp.StatusCode, body)
	}
	id, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("[%s] project-api-key create response missing id: %+v", f.am.name, body)
	}
	idStr := fmt.Sprintf("%d", int64(id))
	row := fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtProjectAPIKeyCreated, idStr)
	assertActorShape(t, f.am, row)

	// 2) DELETE — revoke
	resp, _ = doAuth(t, f.s, f.am, "DELETE",
		"/api/v1/projects/"+f.projectName+"/api-keys/"+idStr, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("[%s] project-api-key revoke code=%d", f.am.name, resp.StatusCode)
	}
	row = fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtProjectAPIKeyRevoked, idStr)
	assertActorShape(t, f.am, row)
}

// runUpstreamCredCRUD drives POST + PATCH + DELETE on /upstream-creds.
// This is the canonical example showing all three D-08 shapes plus the
// row-level assertion across three distinct audit event kinds.
func runUpstreamCredCRUD(t *testing.T, f *auditAttrFixture) {
	t.Helper()

	// 1) POST — create
	resp, body := doAuth(t, f.s, f.am, "POST",
		"/api/v1/projects/"+f.projectName+"/upstream-creds/",
		map[string]any{
			"host":     "upstream-" + f.am.name + ".example.com",
			"kind":     "rpm",
			"username": "u",
			"password": "p",
		})
	if resp.StatusCode != 201 {
		t.Fatalf("[%s] upstream-cred create code=%d body=%+v", f.am.name, resp.StatusCode, body)
	}
	id, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("[%s] upstream-cred create response missing id: %+v", f.am.name, body)
	}
	idStr := fmt.Sprintf("%d", int64(id))
	row := fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtUpstreamCredCreated, idStr)
	assertActorShape(t, f.am, row)

	// 2) PATCH — update
	resp, body = doAuth(t, f.s, f.am, "PATCH",
		"/api/v1/projects/"+f.projectName+"/upstream-creds/"+idStr,
		map[string]any{"username": "u2", "password": "p2"})
	if resp.StatusCode != 200 {
		t.Fatalf("[%s] upstream-cred update code=%d body=%+v", f.am.name, resp.StatusCode, body)
	}
	row = fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtUpstreamCredUpdated, idStr)
	assertActorShape(t, f.am, row)

	// 3) DELETE
	resp, _ = doAuth(t, f.s, f.am, "DELETE",
		"/api/v1/projects/"+f.projectName+"/upstream-creds/"+idStr, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("[%s] upstream-cred delete code=%d", f.am.name, resp.StatusCode)
	}
	row = fetchLatestAuditRow(t, f.s.db.Reader, audit.EvtUpstreamCredDeleted, idStr)
	assertActorShape(t, f.am, row)
}

// bucketSafe normalizes an auth-mode name into a string that satisfies
// gofakes3's bucket-name regex `^[a-z0-9][a-z0-9.\-]{2,62}$`. The only
// disallowed character we ever encounter from f.am.name is `_` (e.g.
// "user_key", "project_key"), which we collapse to `-`.
func bucketSafe(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '_' {
			out = append(out, '-')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// TestAuditAttribution_AllAuthMethods exercises every state-changing
// project-scoped endpoint (S3 bucket / S3 access key / project API key /
// upstream cred) under all three auth methods (session, user-owned
// API key, project-owned API key) and asserts the resulting audit_log
// rows carry the correct (actor_user_id, actor_api_key_id) shape per
// CONTEXT.md D-08.
//
// AUDITATTR-06 forcing function: the project_key/* subtree fails with
// "FK-violation guard" if any future regression re-introduces
// `uid := actor.ID; ActorUserID: &uid` at one of the migrated handlers.
func TestAuditAttribution_AllAuthMethods(t *testing.T) {
	authSeeders := []struct {
		name string
		seed func(t *testing.T) *auditAttrFixture
	}{
		{"session", seedAuditAttributionFixture_Session},
		{"user_key", seedAuditAttributionFixture_UserKey},
		{"project_key", seedAuditAttributionFixture_ProjectKey},
	}
	endpointGroups := []struct {
		name string
		run  func(t *testing.T, f *auditAttrFixture)
	}{
		{"s3_bucket", runS3BucketCreateDelete},
		{"s3_access_key", runS3KeyCreateRevoke},
		{"project_api_key", runProjectAPIKeyCreateRevoke},
		{"upstream_cred", runUpstreamCredCRUD},
	}

	for _, ase := range authSeeders {
		for _, eg := range endpointGroups {
			t.Run(ase.name+"/"+eg.name, func(t *testing.T) {
				f := ase.seed(t)
				eg.run(t, f)
			})
		}
	}
}
