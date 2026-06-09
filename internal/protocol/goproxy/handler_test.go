package goproxy_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/goproxy"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// fixture wires a full GOPROXY handler against tmp data root + sqlitetest
// DB and returns a running httptest.Server.
type fixture struct {
	t        *testing.T
	db       *metadata.DB
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	modules  *metadata.GoModulesRepo
	srv      *httptest.Server
	repoRoot string
	login    string
	password string
	userID   int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	sessions := metadata.NewSessionsRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	modules := metadata.NewGoModulesRepo(db)

	login := "go-user"
	password := "go-test-password-1234567"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash pw: %v", err)
	}
	uid, err := users.Create(context.Background(), login, "g@example.com", hash, false, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	trashRoot := filepath.Join(dataRoot, "trash")
	for _, d := range []string{repoRoot, trashRoot, filepath.Join(dataRoot, "logs")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	auditLogger, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	h := goproxy.New(goproxy.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Sessions:    sessions,
		Repos:       repos,
		Projects:    projects,
		Members:     metadata.NewMembersRepo(db),
		GoModules:   modules,
		Path:        storage.NewPathStore(repoRoot),
		Trash:       storage.NewTrash(trashRoot),
		Audit:       auditLogger,
		MaxPutBytes: 8 << 20,
		RepoRoot:    repoRoot,
	})

	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &fixture{
		t: t, db: db, repos: repos, projects: projects, modules: modules,
		srv: srv, repoRoot: repoRoot, login: login, password: password, userID: uid,
	}
}

// seedRepo creates a project + go repo and enrolls the fixture user.
func (f *fixture) seedRepo(projName, repoName string, publicRead bool) (projectID, repoID int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("seed project: %v", err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, f.userID); err != nil {
		f.t.Fatalf("seed member: %v", err)
	}
	autoScan := false
	rid, err := f.repos.Create(context.Background(), pid, "go", repoName, "", &autoScan, nil, &publicRead)
	if err != nil {
		f.t.Fatalf("seed repo: %v", err)
	}
	return pid, rid
}

func (f *fixture) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password))
}

func (f *fixture) do(t *testing.T, method, urlPath string, body []byte, withAuth bool) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.srv.URL+urlPath, rd)
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, urlPath, err)
	}
	return resp
}

// makeModuleZip builds a spec-valid module zip for modPath@version with a
// go.mod and one trivial source file.
func makeModuleZip(t *testing.T, modPath, version string) []byte {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module " + modPath + "\n\ngo 1.21\n",
		"mini.go": "// Package mini is a test fixture.\npackage mini\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	var buf bytes.Buffer
	if err := modzip.CreateFromDir(&buf, module.Version{Path: modPath, Version: version}, dir); err != nil {
		t.Fatalf("create module zip: %v", err)
	}
	return buf.Bytes()
}

func mustBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestPublishAndFetchRoundtrip(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "gomods", false)
	const mod = "example.com/acme/mini"
	zipBytes := makeModuleZip(t, mod, "v1.0.0")

	// Publish.
	resp := f.do(t, http.MethodPut, "/acme/go/gomods/"+mod+"/@v/v1.0.0.zip", zipBytes, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish status = %d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()

	// /@v/list
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@v/list", nil, true)
	if body := mustBody(t, resp); resp.StatusCode != 200 || body != "v1.0.0\n" {
		t.Fatalf("list = %d %q", resp.StatusCode, body)
	}

	// .info
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@v/v1.0.0.info", nil, true)
	var info struct{ Version string }
	if err := json.Unmarshal([]byte(mustBody(t, resp)), &info); err != nil || info.Version != "v1.0.0" {
		t.Fatalf("info parse: %+v err=%v", info, err)
	}

	// .mod — must carry the go.mod from inside the zip.
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@v/v1.0.0.mod", nil, true)
	if body := mustBody(t, resp); !strings.HasPrefix(body, "module "+mod) {
		t.Fatalf("mod body = %q", body)
	}

	// .zip — byte-for-byte what was uploaded.
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@v/v1.0.0.zip", nil, true)
	if body := mustBody(t, resp); body != string(zipBytes) {
		t.Fatalf("zip roundtrip mismatch (%d vs %d bytes)", len(body), len(zipBytes))
	}

	// @latest
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@latest", nil, true)
	if body := mustBody(t, resp); !strings.Contains(body, `"Version":"v1.0.0"`) {
		t.Fatalf("latest = %q", body)
	}
}

func TestUploadAuthAndValidation(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "gomods", false)
	const mod = "example.com/acme/mini"
	zipBytes := makeModuleZip(t, mod, "v1.0.0")

	// No auth → 401.
	resp := f.do(t, http.MethodPut, "/acme/go/gomods/"+mod+"/@v/v1.0.0.zip", zipBytes, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Garbage zip → 400 invalid_module.
	resp = f.do(t, http.MethodPut, "/acme/go/gomods/"+mod+"/@v/v1.0.0.zip", []byte("not a zip"), true)
	if body := mustBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid_module") {
		t.Errorf("garbage zip = %d %q", resp.StatusCode, body)
	}

	// Zip whose internal prefix names a DIFFERENT module → 400.
	other := makeModuleZip(t, "example.com/acme/other", "v1.0.0")
	resp = f.do(t, http.MethodPut, "/acme/go/gomods/"+mod+"/@v/v1.0.0.zip", other, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong-prefix zip = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Non-canonical version → 400.
	resp = f.do(t, http.MethodPut, "/acme/go/gomods/"+mod+"/@v/1.0.0.zip", zipBytes, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bare version = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Invalid module path (no dot in first element) → 400.
	resp = f.do(t, http.MethodPut, "/acme/go/gomods/notadomain/mini/@v/v1.0.0.zip", zipBytes, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid module path = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Traversal-ish escaped path → 400, nothing written.
	resp = f.do(t, http.MethodPut, "/acme/go/gomods/../../../etc/@v/v1.0.0.zip", zipBytes, true)
	if resp.StatusCode == http.StatusCreated {
		t.Errorf("traversal path accepted: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestEscapedModulePath(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "gomods", false)
	const mod = "example.com/Azure/Thing" // uppercase → !azure/!thing on the wire
	esc, err := module.EscapePath(mod)
	if err != nil {
		t.Fatal(err)
	}
	zipBytes := makeModuleZip(t, mod, "v0.1.0")

	resp := f.do(t, http.MethodPut, "/acme/go/gomods/"+esc+"/@v/v0.1.0.zip", zipBytes, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish escaped = %d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()

	// Row stores the DECODED path.
	row, err := f.modules.FindByModuleVersion(context.Background(), mustRepoID(t, f), mod, "v0.1.0")
	if err != nil || row == nil {
		t.Fatalf("row lookup: %v", err)
	}

	// Fetch via the escaped wire form.
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+esc+"/@v/list", nil, true)
	if body := mustBody(t, resp); body != "v0.1.0\n" {
		t.Fatalf("escaped list = %q", body)
	}
}

func mustRepoID(t *testing.T, f *fixture) int64 {
	t.Helper()
	var id int64
	if err := f.db.Reader.QueryRow(`SELECT id FROM repos WHERE type='go' LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("repo id: %v", err)
	}
	return id
}

func TestListSemverSortAndLatestPrefersRelease(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "gomods", false)
	const mod = "example.com/acme/multi"
	for _, v := range []string{"v0.10.0", "v0.2.0", "v1.0.0", "v1.1.0-rc.1"} {
		resp := f.do(t, http.MethodPut, "/acme/go/gomods/"+mod+"/@v/"+v+".zip", makeModuleZip(t, mod, v), true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("publish %s = %d body=%s", v, resp.StatusCode, mustBody(t, resp))
		}
		_ = resp.Body.Close()
	}

	resp := f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@v/list", nil, true)
	want := "v0.2.0\nv0.10.0\nv1.0.0\nv1.1.0-rc.1\n"
	if body := mustBody(t, resp); body != want {
		t.Errorf("list order = %q, want %q", body, want)
	}

	// @latest prefers the highest RELEASE over a newer pre-release.
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@latest", nil, true)
	if body := mustBody(t, resp); !strings.Contains(body, `"Version":"v1.0.0"`) {
		t.Errorf("latest = %q, want v1.0.0", body)
	}
}

func TestDeleteVersion(t *testing.T) {
	f := newFixture(t)
	_, repoID := f.seedRepo("acme", "gomods", false)
	const mod = "example.com/acme/gone"
	resp := f.do(t, http.MethodPut, "/acme/go/gomods/"+mod+"/@v/v1.0.0.zip", makeModuleZip(t, mod, "v1.0.0"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = f.do(t, http.MethodDelete, "/acme/go/gomods/"+mod+"/@v/v1.0.0", nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()

	// Row gone.
	if _, err := f.modules.FindByModuleVersion(context.Background(), repoID, mod, "v1.0.0"); err == nil {
		t.Errorf("row still present after delete")
	}
	// Zip 404s.
	resp = f.do(t, http.MethodGet, "/acme/go/gomods/"+mod+"/@v/v1.0.0.zip", nil, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("zip after delete = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	// Second delete 404s.
	resp = f.do(t, http.MethodDelete, "/acme/go/gomods/"+mod+"/@v/v1.0.0", nil, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("double delete = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestAnonymousReadFollowsPublicRead(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("pub", "open", true)
	const mod = "example.com/pub/mini"
	resp := f.do(t, http.MethodPut, "/pub/go/open/"+mod+"/@v/v1.0.0.zip", makeModuleZip(t, mod, "v1.0.0"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Anonymous read on a public repo succeeds.
	resp = f.do(t, http.MethodGet, "/pub/go/open/"+mod+"/@v/list", nil, false)
	if body := mustBody(t, resp); resp.StatusCode != 200 || body != "v1.0.0\n" {
		t.Errorf("anon public list = %d %q", resp.StatusCode, body)
	}

	// Private repo: anonymous read is rejected.
	f2 := newFixture(t)
	f2.seedRepo("priv", "closed", false)
	resp = f2.do(t, http.MethodPut, "/priv/go/closed/"+mod+"/@v/v1.0.0.zip", makeModuleZip(t, mod, "v1.0.0"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = f2.do(t, http.MethodGet, "/priv/go/closed/"+mod+"/@v/list", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anon private list = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestGoClientDownload drives the real `go` toolchain against the proxy:
// GOPROXY=<server>/pub/go/open with the sumdb off must let
// `go mod download` fetch a published module into a fresh module cache.
func TestGoClientDownload(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not available")
	}
	f := newFixture(t)
	f.seedRepo("pub", "open", true) // public_read so the client needs no creds
	const mod = "example.com/pub/clientmod"
	resp := f.do(t, http.MethodPut, "/pub/go/open/"+mod+"/@v/v1.2.3.zip", makeModuleZip(t, mod, "v1.2.3"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	work := t.TempDir()
	// The go command write-protects extracted modules, so t.TempDir's
	// cleanup would fail with EACCES — restore write bits before removal.
	cache, err := os.MkdirTemp("", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(cache, func(p string, _ os.DirEntry, err error) error {
			if err == nil {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
		_ = os.RemoveAll(cache)
	})
	cmd := exec.Command(goBin, "mod", "download", mod+"@v1.2.3")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		// GOPRIVATE/GONOSUMDB must stay UNSET — they would route the
		// module around the proxy ("direct"). GOSUMDB=off is sufficient
		// for a private proxy with no sum.golang.org entry.
		"GOPROXY="+f.srv.URL+"/pub/go/open",
		"GOSUMDB=off",
		"GOPRIVATE=", "GONOSUMDB=", "GONOSUMCHECK=",
		"GOFLAGS=-mod=mod",
		"GOMODCACHE="+cache,
		"GOPATH="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go mod download failed: %v\n%s", err, out)
	}
	// The module landed in the cache.
	if _, err := os.Stat(filepath.Join(cache, "cache", "download", "example.com", "pub", "clientmod", "@v", "v1.2.3.zip")); err != nil {
		t.Errorf("module zip not in client cache: %v", err)
	}
}
