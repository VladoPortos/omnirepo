package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// newTestServerWithUpstream is a variant of newTestServer that additionally
// wires the UpstreamCreds repo into api.Deps so the /upstream-creds routes
// are mounted. Phase 1's newTestServer does not include it, and api.Mount
// only mounts the subtree when UpstreamCreds is non-nil.
func newTestServerWithUpstream(t *testing.T) *testServer {
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
	return &testServer{mux: mux, ts: ts, db: db, deps: deps, dataRoot: dataRoot}
}

// credFixture bundles the test server plus three session cookies: alice is a
// member of projA, bob is a member of projB (for cross-project tests), and
// carol is logged in but not a project member.
type credFixture struct {
	s            *testServer
	aliceCookie  string
	bobCookie    string
	carolCookie  string
	projA, projB string
	projAID      int64
}

func setupUpstreamCredFixture(t *testing.T) *credFixture {
	t.Helper()
	pName := "proj-upstream"
	pOther := "proj-other"
	s := newTestServerWithUpstream(t)

	ctx := context.Background()
	projectsRepo := metadata.NewProjectsRepo(s.db)
	membersRepo := metadata.NewMembersRepo(s.db)

	// Users.
	aliceID, alicePW := seedTestUser(t, s.db, "alice-up", "a@x", false, false)
	bobID, bobPW := seedTestUser(t, s.db, "bob-up", "b@x", false, false)
	_, carolPW := seedTestUser(t, s.db, "carol-up", "c@x", false, false)

	// Projects.
	projAID, err := projectsRepo.Create(ctx, pName, "")
	if err != nil {
		t.Fatal(err)
	}
	projBID, err := projectsRepo.Create(ctx, pOther, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := membersRepo.Add(ctx, projAID, aliceID); err != nil {
		t.Fatal(err)
	}
	if err := membersRepo.Add(ctx, projBID, bobID); err != nil {
		t.Fatal(err)
	}

	aliceCookie, _, code := s.login(t, "alice-up", alicePW)
	if code != 200 {
		t.Fatalf("alice login code=%d", code)
	}
	bobCookie, _, code := s.login(t, "bob-up", bobPW)
	if code != 200 {
		t.Fatalf("bob login code=%d", code)
	}
	carolCookie, _, code := s.login(t, "carol-up", carolPW)
	if code != 200 {
		t.Fatalf("carol login code=%d", code)
	}

	return &credFixture{
		s:           s,
		aliceCookie: aliceCookie,
		bobCookie:   bobCookie,
		carolCookie: carolCookie,
		projA:       pName,
		projB:       pOther,
		projAID:     projAID,
	}
}

// doBytes is like testServer.do but returns the raw response bytes so tests
// can inspect for banned substrings.
func (s *testServer) doBytes(t *testing.T, method, path, cookie string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.ts.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	return resp, buf
}

func TestUpstreamCreds_CreateListGetPatchDelete_HappyPath(t *testing.T) {
	f := setupUpstreamCredFixture(t)

	createBody := map[string]any{
		"host":     "registry.example.com",
		"kind":     "docker",
		"username": "alice",
		"password": "VERY-SECRET-PW-123",
	}
	resp, buf := f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie, createBody)
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	assertNoSecret(t, buf)
	var created map[string]any
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	id := int64(created["id"].(float64))
	if id == 0 {
		t.Fatal("no id in response")
	}
	if created["host"] != "registry.example.com" || created["kind"] != "docker" || created["username"] != "alice" {
		t.Fatalf("response missing fields: %s", buf)
	}
	if _, hasPW := created["password"]; hasPW {
		t.Fatalf("response carries password field: %s", buf)
	}

	// List
	resp, buf = f.s.doBytes(t, "GET", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d", resp.StatusCode)
	}
	assertNoSecret(t, buf)

	// Get
	getPath := "/api/v1/projects/" + f.projA + "/upstream-creds/" + itoa(id)
	resp, buf = f.s.doBytes(t, "GET", getPath, f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get code=%d", resp.StatusCode)
	}
	assertNoSecret(t, buf)

	// Patch (rotate password)
	patchBody := map[string]any{
		"username": "alice2",
		"password": "ANOTHER-SECRET-XYZ",
	}
	resp, buf = f.s.doBytes(t, "PATCH", getPath, f.aliceCookie, patchBody)
	if resp.StatusCode != 200 {
		t.Fatalf("patch code=%d body=%s", resp.StatusCode, buf)
	}
	assertNoSecret(t, buf)

	// Delete
	resp, _ = f.s.doBytes(t, "DELETE", getPath, f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete code=%d", resp.StatusCode)
	}
	resp, _ = f.s.doBytes(t, "GET", getPath, f.aliceCookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("post-delete get code=%d, want 404", resp.StatusCode)
	}
}

func TestUpstreamCreds_MissingSecretReturns400(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	body := map[string]any{"host": "h", "kind": "docker", "username": "u"}
	resp, buf := f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie, body)
	if resp.StatusCode != 400 {
		t.Fatalf("code=%d body=%s", resp.StatusCode, buf)
	}
}

func TestUpstreamCreds_MissingHostReturns422(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	body := map[string]any{"kind": "docker", "password": "p"}
	resp, _ := f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie, body)
	if resp.StatusCode != 422 {
		t.Fatalf("code=%d, want 422", resp.StatusCode)
	}
}

func TestUpstreamCreds_InvalidKindReturns422(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	body := map[string]any{"host": "h", "kind": "bogus", "password": "p"}
	resp, _ := f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie, body)
	if resp.StatusCode != 422 {
		t.Fatalf("code=%d, want 422", resp.StatusCode)
	}
}

func TestUpstreamCreds_Unauthenticated_Returns401(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	resp, _ := f.s.doBytes(t, "GET", "/api/v1/projects/"+f.projA+"/upstream-creds/", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("code=%d, want 401", resp.StatusCode)
	}
}

func TestUpstreamCreds_NonMember_Returns403(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	// carol is logged in but not a member of projA.
	resp, _ := f.s.doBytes(t, "GET", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.carolCookie, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("code=%d, want 403", resp.StatusCode)
	}
	// Also POST should 403.
	resp, _ = f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/",
		f.carolCookie, map[string]any{"host": "h", "kind": "docker", "password": "p"})
	if resp.StatusCode != 403 {
		t.Fatalf("post code=%d, want 403", resp.StatusCode)
	}
}

func TestUpstreamCreds_CrossProjectAccess_Returns404(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	// Alice creates a cred in projA.
	resp, buf := f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/",
		f.aliceCookie, map[string]any{"host": "h", "kind": "docker", "username": "u", "password": "p"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	id := int64(created["id"].(float64))

	// Bob (member of projB only) tries to access Alice's cred through projB URL.
	path := "/api/v1/projects/" + f.projB + "/upstream-creds/" + itoa(id)
	resp, _ = f.s.doBytes(t, "GET", path, f.bobCookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-project get code=%d, want 404", resp.StatusCode)
	}
	resp, _ = f.s.doBytes(t, "PATCH", path, f.bobCookie, map[string]any{"password": "x"})
	if resp.StatusCode != 404 {
		t.Fatalf("cross-project patch code=%d, want 404", resp.StatusCode)
	}
	resp, _ = f.s.doBytes(t, "DELETE", path, f.bobCookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-project delete code=%d, want 404", resp.StatusCode)
	}
}

func TestUpstreamCreds_AuditEventsEmitted(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	ctx := context.Background()

	// Create
	resp, buf := f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie,
		map[string]any{"host": "h", "kind": "docker", "username": "u", "password": "PLAINTEXT-SECRET"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	id := int64(created["id"].(float64))

	// Patch
	path := "/api/v1/projects/" + f.projA + "/upstream-creds/" + itoa(id)
	resp, _ = f.s.doBytes(t, "PATCH", path, f.aliceCookie,
		map[string]any{"username": "u2", "password": "ANOTHER-PLAINTEXT"})
	if resp.StatusCode != 200 {
		t.Fatalf("patch code=%d", resp.StatusCode)
	}

	// Delete
	resp, _ = f.s.doBytes(t, "DELETE", path, f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete code=%d", resp.StatusCode)
	}

	// Assert audit rows exist for all three events AND carry no plaintext.
	for _, kind := range []string{"upstream_cred.created", "upstream_cred.updated", "upstream_cred.deleted"} {
		var c int
		if err := f.s.db.Reader.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM audit_log WHERE event_kind=?`, kind).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Fatalf("audit row count for %s = %d, want 1", kind, c)
		}
	}
	// Details never carry plaintext secrets.
	rows, err := f.s.db.Reader.QueryContext(ctx, `SELECT details_json FROM audit_log WHERE event_kind LIKE 'upstream_cred.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var j string
		if err := rows.Scan(&j); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(j, "PLAINTEXT-SECRET") || strings.Contains(j, "ANOTHER-PLAINTEXT") {
			t.Fatalf("audit details leak plaintext: %s", j)
		}
		// Defense-in-depth: no "password" / "token" keys either.
		lj := strings.ToLower(j)
		if strings.Contains(lj, "\"password\"") || strings.Contains(lj, "\"token\"") {
			t.Fatalf("audit details contain password/token key: %s", j)
		}
	}
}

func TestUpstreamCreds_DuplicateHostKindReturns409(t *testing.T) {
	f := setupUpstreamCredFixture(t)
	body := map[string]any{"host": "dup.example.com", "kind": "docker", "username": "u", "password": "p"}
	resp, _ := f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie, body)
	if resp.StatusCode != 201 {
		t.Fatalf("first create code=%d", resp.StatusCode)
	}
	resp, _ = f.s.doBytes(t, "POST", "/api/v1/projects/"+f.projA+"/upstream-creds/", f.aliceCookie, body)
	if resp.StatusCode != 409 {
		t.Fatalf("dup code=%d, want 409", resp.StatusCode)
	}
}

// -------------------------------------------------------------------------
// helpers
// -------------------------------------------------------------------------

func itoa(n int64) string {
	// small dep-free int→string (avoid importing strconv at package level only for this)
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// assertNoSecret checks that the response body does not carry any plaintext
// secret key or our canonical test password.
func assertNoSecret(t *testing.T, body []byte) {
	t.Helper()
	s := strings.ToLower(string(body))
	for _, banned := range []string{
		"\"password\"", "\"token\"", "password_enc", "token_enc",
		"very-secret-pw-123", "another-secret-xyz", "plaintext-secret", "another-plaintext",
	} {
		if strings.Contains(s, strings.ToLower(banned)) {
			t.Fatalf("response contains banned substring %q: %s", banned, body)
		}
	}
}
