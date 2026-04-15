package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

type testServer struct {
	mux      chi.Router
	ts       *httptest.Server
	db       *metadata.DB
	deps     api.Deps
	dataRoot string
}

// seedUser creates a user and returns (id, plaintext password).
func seedTestUser(t *testing.T, db *metadata.DB, login, email string, isSuper, mcp bool) (int64, string) {
	t.Helper()
	pw := "pw-" + login
	hash, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	id, err := metadata.NewUsersRepo(db).Create(context.Background(), login, email, hash, isSuper, mcp)
	if err != nil {
		t.Fatal(err)
	}
	return id, pw
}

func newTestServer(t *testing.T) *testServer {
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

	deps := api.Deps{
		DB:       db,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Repos:    metadata.NewReposRepo(db),
		Settings: metadata.NewSettingsRepo(db),
		Holder:   holder,
		DataRoot: dataRoot,
		Audit:    auditLogger,
		Trash:    storage.NewTrash(filepath.Join(dataRoot, "trash")),
		Locks:    storage.NewLocks(),
	}

	mux := chi.NewRouter()
	mux.Get("/healthz", httpx.Healthz())
	mux.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))
	api.Mount(mux, deps)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &testServer{mux: mux, ts: ts, db: db, deps: deps, dataRoot: dataRoot}
}

// login issues a POST /api/v1/auth/login and returns the session cookie header value.
func (s *testServer) login(t *testing.T, login, pw string) (string, *api.LoginResponse, int) {
	t.Helper()
	body, _ := json.Marshal(api.LoginRequest{Login: login, Password: pw})
	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got api.LoginResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			cookie = c.Value
		}
	}
	return cookie, &got, resp.StatusCode
}

func (s *testServer) do(t *testing.T, method, path, cookie string, body any) (*http.Response, map[string]any) {
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
	out := map[string]any{}
	_ = json.Unmarshal(buf, &out)
	return resp, out
}

// -----------------------------------------------------------------------------
// Healthz / Readyz
// -----------------------------------------------------------------------------

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	resp, err := http.Get(s.ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"status":"ok"`) {
		t.Fatalf("body=%s", b)
	}
}

func TestReadyz(t *testing.T) {
	s := newTestServer(t)
	resp, err := http.Get(s.ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("got %d, expected 200", resp.StatusCode)
	}
}

func TestReadyz_NoCert(t *testing.T) {
	db := sqlitetest.New(t)
	holder := omrtls.NewCertHolder() // empty
	mux := chi.NewRouter()
	mux.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 503 {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Login/Logout/ChangePassword
// -----------------------------------------------------------------------------

func TestLogin_Success(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, resp, code := s.login(t, "alice", "pw-alice")
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	if cookie == "" {
		t.Fatalf("no cookie")
	}
	if resp.Login != "alice" || resp.MustChangePassword {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	_, _, code := s.login(t, "alice", "nope")
	if code != 401 {
		t.Fatalf("code=%d", code)
	}
}

// TestLoginTimingOracle is the WR-04 regression gate: login attempts with
// (a) a nonexistent user, (b) a malformed login, and (c) a real user with a
// wrong password should all take at least one argon2id cost cycle — i.e.
// "unknown user" and "bad login format" must not short-circuit before the
// argon2 verification. We verify the unknown-user path takes at least ~50%
// of a real verify.
func TestLoginTimingOracle(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)

	// Measure a real wrong-password attempt (baseline for argon2 cost).
	realStart := time.Now()
	_, _, code := s.login(t, "alice", "completely-wrong")
	if code != 401 {
		t.Fatalf("wrong pw code=%d", code)
	}
	realDur := time.Since(realStart)

	// Measure unknown user; should be comparable (within 50% of real).
	unknownStart := time.Now()
	_, _, code = s.login(t, "ghostuser", "anypassword")
	if code != 401 {
		t.Fatalf("unknown user code=%d", code)
	}
	unknownDur := time.Since(unknownStart)

	// The unknown-user path MUST burn argon2 so it takes a substantial
	// fraction of the real verify time. Without the fix, unknown users
	// return in microseconds (zero DB match, no argon2 call). We allow
	// noise tolerance: require at least 50% of real.
	minExpected := realDur / 2
	if unknownDur < minExpected {
		t.Fatalf("unknown-user login too fast (%v) vs real (%v); timing oracle present",
			unknownDur, realDur)
	}

	// Also check that a malformed-login response path still takes some
	// argon2 time (not the earlier microsecond-LoginValid short-circuit).
	badFmtStart := time.Now()
	_, _, code = s.login(t, "has spaces & symbols!!!", "x")
	if code != 401 {
		t.Fatalf("malformed login code=%d", code)
	}
	badFmtDur := time.Since(badFmtStart)
	if badFmtDur < minExpected {
		t.Fatalf("malformed-login too fast (%v) vs real (%v); timing oracle present",
			badFmtDur, realDur)
	}
}

func TestLogin_MCPUser(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "carol", "c@x", false, true)
	cookie, resp, code := s.login(t, "carol", "pw-carol")
	if code != 200 || cookie == "" || !resp.MustChangePassword {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestLogout(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", "pw-alice")
	resp, _ := s.do(t, "POST", "/api/v1/auth/logout", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("logout code=%d", resp.StatusCode)
	}
	// Subsequent /me with stale cookie → 401.
	resp2, _ := s.do(t, "GET", "/api/v1/me", cookie, nil)
	if resp2.StatusCode != 401 {
		t.Fatalf("expected 401 after logout, got %d", resp2.StatusCode)
	}
}

func TestChangePassword(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, true) // MCP user
	cookie, _, _ := s.login(t, "alice", "pw-alice")
	resp, _ := s.do(t, "POST", "/api/v1/auth/change-password", cookie, api.ChangePasswordRequest{Current: "pw-alice", New: "newsecret"})
	if resp.StatusCode != 200 {
		t.Fatalf("change pw code=%d", resp.StatusCode)
	}
	// New password works; old does not.
	_, _, code := s.login(t, "alice", "newsecret")
	if code != 200 {
		t.Fatalf("new pw login code=%d", code)
	}
	_, _, code = s.login(t, "alice", "pw-alice")
	if code != 401 {
		t.Fatalf("old pw should fail, got %d", code)
	}
	// MCP flag cleared.
	u, _ := s.deps.Users.FindByLogin(context.Background(), "alice")
	if u.MustChangePassword {
		t.Fatalf("MCP flag should be cleared")
	}
}

func TestChangePassword_WrongCurrent(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", "pw-alice")
	resp, _ := s.do(t, "POST", "/api/v1/auth/change-password", cookie, api.ChangePasswordRequest{Current: "wrong", New: "x"})
	if resp.StatusCode != 401 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// /me + DELETE /me
// -----------------------------------------------------------------------------

func TestMe(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", "pw-alice")
	resp, body := s.do(t, "GET", "/api/v1/me", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	if body["login"] != "alice" {
		t.Fatalf("body=%+v", body)
	}
}

func TestMe_Unauthenticated(t *testing.T) {
	s := newTestServer(t)
	resp, _ := s.do(t, "GET", "/api/v1/me", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
}

func TestDeleteMe(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", "pw-alice")
	resp, _ := s.do(t, "DELETE", "/api/v1/me", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	resp2, _ := s.do(t, "GET", "/api/v1/me", cookie, nil)
	if resp2.StatusCode != 401 {
		t.Fatalf("expected 401 after delete, got %d", resp2.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Admin users
// -----------------------------------------------------------------------------

func TestCreateUser(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	resp, body := s.do(t, "POST", "/api/v1/admin/users", cookie, api.CreateUserRequest{Login: "bob", Email: "b@x"})
	if resp.StatusCode != 200 {
		t.Fatalf("code=%d body=%+v", resp.StatusCode, body)
	}
	otp, _ := body["one_time_password"].(string)
	if len(otp) != 16 {
		t.Fatalf("OTP len=%d", len(otp))
	}
	// Created user has MCP=true.
	u, _ := s.deps.Users.FindByLogin(context.Background(), "bob")
	if !u.MustChangePassword {
		t.Fatalf("new user should have MCP=true")
	}
}

func TestCreateUser_ForbiddenForNonSuper(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", "pw-alice")
	resp, _ := s.do(t, "POST", "/api/v1/admin/users", cookie, api.CreateUserRequest{Login: "x", Email: "x@x"})
	if resp.StatusCode != 403 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
}

func TestDeleteUser(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "target", "t@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	resp, _ := s.do(t, "DELETE", "/api/v1/admin/users/target", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	resp2, _ := s.do(t, "DELETE", "/api/v1/admin/users/ghost", cookie, nil)
	if resp2.StatusCode != 404 {
		t.Fatalf("code=%d", resp2.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Projects / members / repos
// -----------------------------------------------------------------------------

func TestProjectCRUD(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	// Create.
	resp, _ := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "myproj"})
	if resp.StatusCode != 200 {
		t.Fatalf("create code=%d", resp.StatusCode)
	}
	// Reserved name.
	resp, _ = s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "api"})
	if resp.StatusCode != 422 {
		t.Fatalf("reserved code=%d", resp.StatusCode)
	}
	// Duplicate.
	resp, _ = s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "myproj"})
	if resp.StatusCode != 409 {
		t.Fatalf("dup code=%d", resp.StatusCode)
	}
	// Delete.
	resp, _ = s.do(t, "DELETE", "/api/v1/projects/myproj", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("del code=%d", resp.StatusCode)
	}
}

func TestMembers(t *testing.T) {
	s := newTestServer(t)
	sid, _ := seedTestUser(t, s.db, "super", "s@x", true, false)
	uid, _ := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	_, _ = sid, uid
	// super creates project; super is auto-added as member.
	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "p"})
	resp, _ := s.do(t, "POST", "/api/v1/projects/p/members/alice", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("add member code=%d", resp.StatusCode)
	}
	resp, _ = s.do(t, "DELETE", "/api/v1/projects/p/members/alice", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("remove code=%d", resp.StatusCode)
	}
	// Non-member caller forbidden.
	aliceCookie, _, _ := s.login(t, "alice", "pw-alice")
	resp, _ = s.do(t, "POST", "/api/v1/projects/p/members/alice", aliceCookie, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-member add code=%d", resp.StatusCode)
	}
}

func TestRepoCRUD(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "pr"})

	// Create each of the 7 types.
	for _, typ := range []string{"rpm", "deb", "pypi", "docker", "helm", "git", "raw"} {
		resp, body := s.do(t, "POST", "/api/v1/projects/pr/repos", cookie, api.CreateRepoRequest{Name: "r1", Type: typ})
		if resp.StatusCode != 200 {
			t.Fatalf("create %s code=%d body=%+v", typ, resp.StatusCode, body)
		}
	}
	// Invalid type.
	resp, _ := s.do(t, "POST", "/api/v1/projects/pr/repos", cookie, api.CreateRepoRequest{Name: "x", Type: "bogus"})
	if resp.StatusCode != 422 {
		t.Fatalf("bogus code=%d", resp.StatusCode)
	}
	// Duplicate.
	resp, _ = s.do(t, "POST", "/api/v1/projects/pr/repos", cookie, api.CreateRepoRequest{Name: "r1", Type: "docker"})
	if resp.StatusCode != 409 {
		t.Fatalf("dup code=%d", resp.StatusCode)
	}
}

// TestRepoDelete_MissingOnDiskTreeSucceeds is the WR-06 regression gate
// for the errors.Is(os.ErrNotExist) replacement of the brittle
// strings.Contains(err.Error(), "no such file") check. A repo that has
// no on-disk tree yet (freshly-created, never uploaded to) must
// delete cleanly with no trash_move_failed audit event.
func TestRepoDelete_MissingOnDiskTreeSucceeds(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "prn"})
	s.do(t, "POST", "/api/v1/projects/prn/repos", cookie, api.CreateRepoRequest{Name: "r1", Type: "docker"})

	// Do NOT pre-create the on-disk tree; trash.Move will get ENOENT.
	resp, _ := s.do(t, "DELETE", "/api/v1/projects/prn/repos/docker/r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	// No trash_move_failed audit (ENOENT is recognized via errors.Is).
	var n int
	_ = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind='repo.deleted' AND outcome='trash_move_failed'`).Scan(&n)
	if n != 0 {
		t.Fatalf("ENOENT on missing tree should not surface as trash_move_failed; got %d rows", n)
	}
}

func TestRepoDelete_MovesTreeToTrash(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "pr"})
	s.do(t, "POST", "/api/v1/projects/pr/repos", cookie, api.CreateRepoRequest{Name: "r1", Type: "docker"})
	// Create synthetic tree.
	onDisk := filepath.Join(s.dataRoot, "repos", "pr", "docker", "r1")
	if err := os.MkdirAll(onDisk, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onDisk, "blob"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, _ := s.do(t, "DELETE", "/api/v1/projects/pr/repos/docker/r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	// Tree moved to trash.
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Fatalf("expected original gone: %v", err)
	}
	trashEntries, _ := os.ReadDir(filepath.Join(s.dataRoot, "trash"))
	if len(trashEntries) == 0 {
		t.Fatalf("expected trash entries")
	}
	// audit_log has a repo.deleted row.
	var n int
	_ = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind='repo.deleted'`).Scan(&n)
	if n == 0 {
		t.Fatalf("no repo.deleted audit row")
	}
}

// -----------------------------------------------------------------------------
// MCP 403 matrix (Pitfall P5 layer 3)
// -----------------------------------------------------------------------------

func TestMCPRESTReturns403OnEveryMutation(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "mcp", "m@x", false, true) // MCP user, NOT super
	cookie, _, _ := s.login(t, "mcp", "pw-mcp")

	type tc struct {
		method, path string
		body         any
	}
	forbidden := []tc{
		{"POST", "/api/v1/admin/users", api.CreateUserRequest{Login: "x", Email: "x"}},
		{"DELETE", "/api/v1/admin/users/whoever", nil},
		{"POST", "/api/v1/projects", api.CreateProjectRequest{Name: "p"}},
		{"DELETE", "/api/v1/projects/p", nil},
		{"POST", "/api/v1/projects/p/members/x", nil},
		{"DELETE", "/api/v1/projects/p/members/x", nil},
		{"POST", "/api/v1/projects/p/repos", api.CreateRepoRequest{Name: "r", Type: "docker"}},
		{"DELETE", "/api/v1/projects/p/repos/docker/r", nil},
		{"POST", "/api/v1/admin/tls/upload", nil},
		{"DELETE", "/api/v1/me", nil},
	}
	for _, c := range forbidden {
		resp, body := s.do(t, c.method, c.path, cookie, c.body)
		if resp.StatusCode != 403 {
			t.Errorf("%s %s: code=%d body=%+v", c.method, c.path, resp.StatusCode, body)
			continue
		}
		if body["error"] != "password-change-required" {
			t.Errorf("%s %s: error=%v", c.method, c.path, body["error"])
		}
	}
	// Allowed for MCP: change-password and logout.
	resp, _ := s.do(t, "POST", "/api/v1/auth/logout", cookie, nil)
	if resp.StatusCode != 200 {
		t.Errorf("logout denied for MCP: %d", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// TLS upload
// -----------------------------------------------------------------------------

func TestTLSUpload(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")

	// Build multipart form.
	certPEM, keyPEM, err := omrtls.GenerateSelfSigned([]string{"uploaded.example"}, time.Hour, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	certPart, _ := w.CreateFormFile("cert", "server.crt")
	_, _ = certPart.Write(certPEM)
	keyPart, _ := w.CreateFormFile("key", "server.key")
	_, _ = keyPart.Write(keyPEM)
	_ = w.Close()

	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/admin/tls/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("code=%d body=%s", resp.StatusCode, b)
	}
	// Live file updated.
	live, _ := os.ReadFile(filepath.Join(s.dataRoot, "certs", "server.crt"))
	if string(live) != string(certPEM) {
		t.Fatalf("live cert not updated")
	}
	// audit row present.
	var n int
	_ = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind='tls.cert.uploaded'`).Scan(&n)
	if n == 0 {
		t.Fatalf("no tls.cert.uploaded audit")
	}
}

// -----------------------------------------------------------------------------
// Success criteria (ROADMAP) — simplified end-to-end
// -----------------------------------------------------------------------------

func TestSuccessCriterion1(t *testing.T) {
	// Both /healthz and /readyz reachable on the same server.
	s := newTestServer(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(s.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s code=%d", p, resp.StatusCode)
		}
	}
}

func TestSuccessCriterion2(t *testing.T) {
	// super creates project + each of 7 repos via REST; MCP user 403's on every mutation.
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	seedTestUser(t, s.db, "bob", "b@x", false, true) // MCP
	cookie, _, _ := s.login(t, "super", "pw-super")

	resp, _ := s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "newp"})
	if resp.StatusCode != 200 {
		t.Fatalf("create p: %d", resp.StatusCode)
	}
	for _, typ := range []string{"rpm", "deb", "pypi", "docker", "helm", "git", "raw"} {
		name := fmt.Sprintf("r-%s", typ)
		resp, body := s.do(t, "POST", "/api/v1/projects/newp/repos", cookie, api.CreateRepoRequest{Name: name, Type: typ})
		if resp.StatusCode != 200 {
			t.Fatalf("create %s code=%d body=%+v", typ, resp.StatusCode, body)
		}
	}
	// MCP user blocked.
	bobCookie, _, _ := s.login(t, "bob", "pw-bob")
	resp, body := s.do(t, "POST", "/api/v1/projects", bobCookie, api.CreateProjectRequest{Name: "x"})
	if resp.StatusCode != 403 || body["error"] != "password-change-required" {
		t.Fatalf("MCP REST not blocked: code=%d body=%+v", resp.StatusCode, body)
	}
}

func TestSuccessCriterion5(t *testing.T) {
	// Same as TestRepoDelete_MovesTreeToTrash but with audit.log NDJSON check.
	s := newTestServer(t)
	seedTestUser(t, s.db, "super", "s@x", true, false)
	cookie, _, _ := s.login(t, "super", "pw-super")
	s.do(t, "POST", "/api/v1/projects", cookie, api.CreateProjectRequest{Name: "prj"})
	s.do(t, "POST", "/api/v1/projects/prj/repos", cookie, api.CreateRepoRequest{Name: "r1", Type: "docker"})
	onDisk := filepath.Join(s.dataRoot, "repos", "prj", "docker", "r1")
	_ = os.MkdirAll(onDisk, 0o750)
	_ = os.WriteFile(filepath.Join(onDisk, "blob"), []byte("x"), 0o644)

	resp, _ := s.do(t, "DELETE", "/api/v1/projects/prj/repos/docker/r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete code=%d", resp.StatusCode)
	}
	// audit_log row.
	var n int
	_ = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind='repo.deleted'`).Scan(&n)
	if n == 0 {
		t.Fatalf("no repo.deleted in audit_log")
	}
	// NDJSON mirror.
	ndjson, _ := os.ReadFile(filepath.Join(s.dataRoot, "logs", "audit.log"))
	if !strings.Contains(string(ndjson), "repo.deleted") {
		t.Fatalf("audit.log missing repo.deleted: %s", ndjson)
	}
}
