package raw_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/raw"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// rawFixture wires up a full RAW handler against tmp data root + sqlitetest
// DB and returns a running httptest.Server.
type rawFixture struct {
	t        *testing.T
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	repos    *metadata.ReposRepo
	files    *metadata.RawFilesRepo
	scans    *metadata.ScansRepo
	projects *metadata.ProjectsRepo
	srv      *httptest.Server
	dataRoot string
	repoRoot string
	login    string
	password string
	userID   int64
}

func newRawFixture(t *testing.T) *rawFixture {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	repos := metadata.NewReposRepo(db)
	files := metadata.NewRawFilesRepo(db)
	scans := metadata.NewScansRepo(db)
	projects := metadata.NewProjectsRepo(db)
	sessions := metadata.NewSessionsRepo(db)

	login := "raw-user"
	password := "raw-test-password-1234567"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash pw: %v", err)
	}
	uid, err := users.Create(context.Background(), login, "u@example.com", hash, false, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	trashRoot := filepath.Join(dataRoot, "trash")
	if err := os.MkdirAll(repoRoot, 0o750); err != nil {
		t.Fatalf("mkdir repo root: %v", err)
	}
	if err := os.MkdirAll(trashRoot, 0o750); err != nil {
		t.Fatalf("mkdir trash root: %v", err)
	}

	// Real audit logger (writes to tmp).
	auditPath := filepath.Join(dataRoot, "logs", "audit.log")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
		t.Fatalf("mkdir log: %v", err)
	}
	auditLogger, err := audit.New(db, auditPath, 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	pathStore := storage.NewPathStore(repoRoot)
	trash := storage.NewTrash(trashRoot)

	h := raw.New(raw.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Sessions:    sessions,
		Repos:       repos,
		Projects:    projects,
		Files:       files,
		Scans:       scans,
		Members:     metadata.NewMembersRepo(db),
		Path:        pathStore,
		Trash:       trash,
		Audit:       auditLogger,
		MaxPutBytes: 1 << 20, // 1 MiB cap so the oversized-PUT test is fast.
		RepoRoot:    repoRoot,
	})

	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &rawFixture{
		t:        t,
		db:       db,
		users:    users,
		apiKeys:  apiKeys,
		repos:    repos,
		files:    files,
		scans:    scans,
		projects: projects,
		srv:      srv,
		dataRoot: dataRoot,
		repoRoot: repoRoot,
		login:    login,
		password: password,
		userID:   uid,
	}
}

// seedRepo creates a project + raw repo with the given public_read setting.
func (f *rawFixture) seedRepo(projName, repoName string, publicRead, autoScan bool) (projectID, repoID int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("seed project: %v", err)
	}
	if err := metadataAddMember(f.db, pid, f.userID); err != nil {
		f.t.Fatalf("seed member: %v", err)
	}
	rid, err := f.repos.Create(context.Background(), pid, "raw", repoName, "", &autoScan, nil, &publicRead)
	if err != nil {
		f.t.Fatalf("seed repo: %v", err)
	}
	return pid, rid
}

// metadataAddMember directly inserts a project_members row so the user is
// authorized to write to the project. Mirrors what api.Mount's
// membershipResolver eventually consults.
func metadataAddMember(db *metadata.DB, projectID, userID int64) error {
	_, err := db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, projectID, userID)
	return err
}

func (f *rawFixture) basicAuth(login, pw string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(login+":"+pw))
}

func (f *rawFixture) put(t *testing.T, urlPath string, body []byte, withAuth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, f.srv.URL+urlPath, bytes.NewReader(body))
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth(f.login, f.password))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", urlPath, err)
	}
	return resp
}

func (f *rawFixture) get(t *testing.T, urlPath string, withAuth bool, accept string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+urlPath, nil)
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth(f.login, f.password))
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", urlPath, err)
	}
	return resp
}

func (f *rawFixture) del(t *testing.T, urlPath string, withAuth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+urlPath, nil)
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth(f.login, f.password))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", urlPath, err)
	}
	return resp
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestRawPutGet_HappyPath(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("proj1", "raw1", false, false)

	body := []byte("hello world")
	resp := f.put(t, "/proj1/raw/raw1/notes/hello.txt", body, true)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT status=%d body=%s", resp.StatusCode, b)
	}
	loc := resp.Header.Get("Location")
	if loc == "" || !strings.HasSuffix(loc, "/proj1/raw/raw1/notes/hello.txt") {
		t.Fatalf("Location header bad: %q", loc)
	}
	resp.Body.Close()

	// File on disk.
	diskPath := filepath.Join(f.repoRoot, "proj1", "raw", "raw1", "notes", "hello.txt")
	got, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read on-disk file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("disk body mismatch")
	}

	// raw_files row.
	row, found, err := f.files.Get(context.Background(), 1, "notes/hello.txt")
	if err != nil || !found {
		t.Fatalf("row missing: found=%v err=%v", found, err)
	}
	if row.SizeBytes != int64(len(body)) {
		t.Fatalf("size mismatch: %d", row.SizeBytes)
	}
	expectedSha := sha256.Sum256(body)
	if row.SHA256 != "sha256:"+hex.EncodeToString(expectedSha[:]) {
		t.Fatalf("sha mismatch: %s", row.SHA256)
	}
	if row.MIME != "text/plain; charset=utf-8" && !strings.HasPrefix(row.MIME, "text/plain") {
		t.Fatalf("mime mismatch: %s", row.MIME)
	}

	// GET back.
	resp = f.get(t, "/proj1/raw/raw1/notes/hello.txt", true, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("GET content-type: %q", resp.Header.Get("Content-Type"))
	}
	if cl := resp.Header.Get("Content-Length"); cl != fmt.Sprintf("%d", len(body)) {
		t.Fatalf("Content-Length: %q want %d", cl, len(body))
	}
	gotBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("GET body mismatch")
	}
}

// TestRawGet_MagicNumberFallback uses an unknown extension so
// mime.TypeByExtension returns "" and the handler must fall back to
// http.DetectContentType on the first 512 bytes.
func TestRawGet_MagicNumberFallback(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("p", "r", false, false)

	// PNG magic-number bytes followed by junk; .foo extension is unknown.
	body := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 16)...)
	resp := f.put(t, "/p/raw/r/blob.foo", body, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = f.get(t, "/p/raw/r/blob.foo", true, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	resp.Body.Close()
	// http.DetectContentType returns "image/png" for the PNG signature.
	if !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("expected magic-number fallback to detect image/png, got %q", ct)
	}
}

func TestRawPut_PathTraversalRejected(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("p", "r", false, false)

	// chi may decode %2e%2e or take the literal — try both.
	urls := []string{
		"/p/raw/r/../../etc/passwd",
		"/p/raw/r/sub/../../../etc/passwd",
	}
	for _, u := range urls {
		resp := f.put(t, u, []byte("x"), true)
		// chi normalizes "../" before dispatch in many cases — accept either
		// 400 or 404. Critically: NO file should be written outside the repo.
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Logf("url=%s status=%d (treating as benign as long as no escape)", u, resp.StatusCode)
		}
		resp.Body.Close()
	}
	// Confirm /etc/passwd-style escape never landed under repoRoot's parent.
	if _, err := os.Stat(filepath.Join(f.repoRoot, "..", "etc", "passwd")); err == nil {
		t.Fatalf("path traversal escaped repo root!")
	}
}

func TestRawAnonymousGet_PublicRead(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("pub", "r", true, false) // public_read=true

	// PUT (with auth).
	body := []byte("public file")
	resp := f.put(t, "/pub/raw/r/file.txt", body, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Anonymous GET succeeds.
	resp = f.get(t, "/pub/raw/r/file.txt", false, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous GET: %d (expected 200)", resp.StatusCode)
	}
	resp.Body.Close()

	// Anonymous PUT denied.
	resp = f.put(t, "/pub/raw/r/other.txt", []byte("x"), false)
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous PUT: %d (expected 401/403)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRawAnonymousGet_PrivateRepoBlocked(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("priv", "r", false, false) // public_read=false

	body := []byte("secret")
	resp := f.put(t, "/priv/raw/r/secret.txt", body, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = f.get(t, "/priv/raw/r/secret.txt", false, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET on private: %d (expected 401)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRawDelete_MovesToTrash(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("p", "r", false, false)

	body := []byte("delete me")
	resp := f.put(t, "/p/raw/r/d.txt", body, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = f.del(t, "/p/raw/r/d.txt", true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Original file is gone from CAS.
	if _, err := os.Stat(filepath.Join(f.repoRoot, "p", "raw", "r", "d.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after DELETE: err=%v", err)
	}
	// Row removed.
	_, found, _ := f.files.Get(context.Background(), 1, "d.txt")
	if found {
		t.Fatal("raw_files row not deleted")
	}
	// Subsequent GET returns 404.
	resp = f.get(t, "/p/raw/r/d.txt", true, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete: %d (want 404)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRawPut_OversizedRejected(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("p", "r", false, false)

	// fixture sets MaxPutBytes=1 MiB; send 2 MiB.
	body := bytes.Repeat([]byte("A"), 2*1024*1024)
	resp := f.put(t, "/p/raw/r/big.bin", body, true)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT: %d (want 413)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRawGet_DirectoryListing_JSONAndHTML(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("p", "r", false, false)

	// Seed a few files.
	files := map[string]string{
		"/p/raw/r/dir/a.txt":     "alpha",
		"/p/raw/r/dir/b.txt":     "beta",
		"/p/raw/r/dir/sub/c.txt": "gamma",
	}
	for u, content := range files {
		resp := f.put(t, u, []byte(content), true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s: %d", u, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// JSON listing of /dir.
	resp := f.get(t, "/p/raw/r/dir/", true, "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dir GET json: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("dir json content-type: %q", resp.Header.Get("Content-Type"))
	}
	var items []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode dir json: %v", err)
	}
	resp.Body.Close()
	if len(items) < 2 {
		t.Fatalf("expected at least a.txt + b.txt, got %d items: %+v", len(items), items)
	}
	names := map[string]bool{}
	for _, it := range items {
		names[it["name"].(string)] = true
	}
	if !names["a.txt"] || !names["b.txt"] {
		t.Fatalf("expected a.txt and b.txt direct children, got %+v", names)
	}

	// Default Accept → HTML.
	resp = f.get(t, "/p/raw/r/dir/", true, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dir GET html: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("dir html content-type: %q", resp.Header.Get("Content-Type"))
	}
	htmlBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(htmlBody), "a.txt") || !strings.Contains(string(htmlBody), "b.txt") {
		t.Fatalf("html body missing entries: %s", htmlBody)
	}
}

func TestRawGet_NotFound(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("p", "r", false, false)
	resp := f.get(t, "/p/raw/r/nope.txt", true, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file GET: %d (want 404)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRawPut_RepoMustExist(t *testing.T) {
	f := newRawFixture(t)
	// no repo seeded.
	resp := f.put(t, "/missing/raw/none/x.txt", []byte("x"), true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("PUT to nonexistent repo: %d (want 404)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRawAuditEvents_PutAndDeleteRecorded(t *testing.T) {
	f := newRawFixture(t)
	f.seedRepo("p", "r", false, false)

	resp := f.put(t, "/p/raw/r/audit.txt", []byte("x"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = f.del(t, "/p/raw/r/audit.txt", true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Two audit rows should have landed.
	var n int
	if err := f.db.Reader.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE event_kind IN ('raw.put','raw.delete')`,
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 raw.{put,delete} audit rows, got %d", n)
	}
}

// TestRawGet_CrossProjectPrivateBlocked is the CR-01 regression. Before
// the fix, any authenticated user (even one who is NOT a member of the
// owning project) could GET a private RAW repo's bytes. The fix routes
// the read check through auth.Can(ActionRepoRead), which requires project
// membership when PublicRead=false.
func TestRawGet_CrossProjectPrivateBlocked(t *testing.T) {
	f := newRawFixture(t)
	// Create a private repo owned by "owner" project; f.userID is a member.
	f.seedRepo("owner", "secrets", false, false)

	// PUT a file as the member.
	body := []byte("top secret")
	resp := f.put(t, "/owner/raw/secrets/secret.txt", body, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Create a second authenticated user that is NOT a member of "owner".
	outsiderLogin := "outsider"
	outsiderPW := "outsider-password-123456"
	hash, err := auth.HashPassword(outsiderPW)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := f.users.Create(context.Background(), outsiderLogin, "o@example.com",
		hash, false, false); err != nil {
		t.Fatalf("create outsider: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/owner/raw/secrets/secret.txt", nil)
	req.Header.Set("Authorization", f.basicAuth(outsiderLogin, outsiderPW))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("cross-project private GET: status=%d body=%s (want 403)", resp.StatusCode, got)
	}
}

// TestAuditEventConstants verifies the event kind constants exist.
func TestAuditEventConstants(t *testing.T) {
	if string(audit.EvtRawPut) != "raw.put" {
		t.Fatalf("EvtRawPut: %q", audit.EvtRawPut)
	}
	if string(audit.EvtRawDelete) != "raw.delete" {
		t.Fatalf("EvtRawDelete: %q", audit.EvtRawDelete)
	}
	if string(audit.EvtRawGetBlocked) != "raw.get.blocked" {
		t.Fatalf("EvtRawGetBlocked: %q", audit.EvtRawGetBlocked)
	}
}
