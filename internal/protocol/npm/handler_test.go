package npm_test

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // npm wire format
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	npmpkg "github.com/vladoportos/omnirepo/internal/protocol/npm"
	"github.com/vladoportos/omnirepo/internal/storage"
)

type fixture struct {
	t        *testing.T
	db       *metadata.DB
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	packages *metadata.NPMPackagesRepo
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
	packages := metadata.NewNPMPackagesRepo(db)

	login := "npm-user"
	password := "npm-test-password-1234567"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash pw: %v", err)
	}
	uid, err := users.Create(context.Background(), login, "n@example.com", hash, false, false)
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

	h := npmpkg.New(npmpkg.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Sessions:    sessions,
		Repos:       repos,
		Projects:    projects,
		Members:     metadata.NewMembersRepo(db),
		Packages:    packages,
		Path:        storage.NewPathStore(repoRoot),
		Trash:       storage.NewTrash(trashRoot),
		Audit:       auditLogger,
		MaxPutBytes: 32 << 20,
		RepoRoot:    repoRoot,
	})

	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &fixture{
		t: t, db: db, repos: repos, projects: projects, packages: packages,
		srv: srv, repoRoot: repoRoot, login: login, password: password, userID: uid,
	}
}

func (f *fixture) seedRepo(projName, repoName string, publicRead bool) (projectID, repoID int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("seed project: %v", err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, f.userID); err != nil {
		f.t.Fatalf("seed member: %v", err)
	}
	autoScan := false
	rid, err := f.repos.Create(context.Background(), pid, "npm", repoName, "", &autoScan, nil, &publicRead)
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
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, urlPath, err)
	}
	return resp
}

// publishJSON builds the document `npm publish` PUTs.
func publishJSON(t *testing.T, name, version string, tarball []byte) []byte {
	t.Helper()
	sum := sha1.Sum(tarball) //nolint:gosec // npm wire format
	manifest := map[string]any{
		"name":        name,
		"version":     version,
		"description": "test fixture",
		"dist": map[string]any{
			"shasum": hex.EncodeToString(sum[:]),
		},
	}
	base := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		base = name[i+1:]
	}
	doc := map[string]any{
		"name":      name,
		"dist-tags": map[string]string{"latest": version},
		"versions":  map[string]any{version: manifest},
		"_attachments": map[string]any{
			base + "-" + version + ".tgz": map[string]any{
				"content_type": "application/octet-stream",
				"data":         base64.StdEncoding.EncodeToString(tarball),
				"length":       len(tarball),
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
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
	f.seedRepo("acme", "js", false)
	tarball := []byte("fake-tarball-bytes-roundtrip")

	resp := f.do(t, http.MethodPut, "/acme/npm/js/mini-lib", publishJSON(t, "mini-lib", "1.0.0", tarball), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()

	// Packument carries dist-tags + our tarball URL + integrity.
	resp = f.do(t, http.MethodGet, "/acme/npm/js/mini-lib", nil, true)
	var pack struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct{ Tarball, Shasum, Integrity string }
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(mustBody(t, resp)), &pack); err != nil {
		t.Fatalf("packument parse: %v", err)
	}
	if pack.DistTags["latest"] != "1.0.0" {
		t.Errorf("latest = %q", pack.DistTags["latest"])
	}
	v, ok := pack.Versions["1.0.0"]
	if !ok {
		t.Fatalf("version missing from packument")
	}
	if !strings.Contains(v.Dist.Tarball, "/acme/npm/js/mini-lib/-/mini-lib-1.0.0.tgz") {
		t.Errorf("tarball URL = %q", v.Dist.Tarball)
	}
	wantSum := sha1.Sum(tarball) //nolint:gosec // npm wire format
	if v.Dist.Shasum != hex.EncodeToString(wantSum[:]) {
		t.Errorf("shasum mismatch")
	}

	// Tarball download is byte-identical.
	resp = f.do(t, http.MethodGet, "/acme/npm/js/mini-lib/-/mini-lib-1.0.0.tgz", nil, true)
	if body := mustBody(t, resp); body != string(tarball) {
		t.Fatalf("tarball roundtrip mismatch")
	}

	// Republishing the same version is rejected (immutability).
	resp = f.do(t, http.MethodPut, "/acme/npm/js/mini-lib", publishJSON(t, "mini-lib", "1.0.0", tarball), true)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("republish = %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestScopedPackage(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "js", false)
	tarball := []byte("scoped-bytes")

	// npm sends the scope slash URL-encoded.
	resp := f.do(t, http.MethodPut, "/acme/npm/js/@acme%2fui-kit", publishJSON(t, "@acme/ui-kit", "2.1.0", tarball), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("scoped publish = %d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()

	// Fetch via both spellings.
	for _, p := range []string{"/acme/npm/js/@acme%2fui-kit", "/acme/npm/js/@acme/ui-kit"} {
		resp = f.do(t, http.MethodGet, p, nil, true)
		if body := mustBody(t, resp); resp.StatusCode != 200 || !strings.Contains(body, `"2.1.0"`) {
			t.Errorf("packument via %s = %d", p, resp.StatusCode)
		}
	}
	// Tarball path embeds the scope dir but the file is scope-less.
	resp = f.do(t, http.MethodGet, "/acme/npm/js/@acme/ui-kit/-/ui-kit-2.1.0.tgz", nil, true)
	if body := mustBody(t, resp); body != string(tarball) {
		t.Fatalf("scoped tarball mismatch")
	}
}

func TestPublishValidation(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "js", false)
	tarball := []byte("x")

	// Unauthenticated.
	resp := f.do(t, http.MethodPut, "/acme/npm/js/mini-lib", publishJSON(t, "mini-lib", "1.0.0", tarball), false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// URL/name mismatch.
	resp = f.do(t, http.MethodPut, "/acme/npm/js/other-name", publishJSON(t, "mini-lib", "1.0.0", tarball), true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("name mismatch = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Shasum mismatch.
	doc := publishJSON(t, "mini-lib", "1.0.0", tarball)
	bad := bytes.Replace(doc, []byte(`"shasum":"`), []byte(`"shasum":"00`), 1)
	resp = f.do(t, http.MethodPut, "/acme/npm/js/mini-lib", bad, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("shasum mismatch = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Invalid names: uppercase, traversal, bare scope.
	for _, name := range []string{"UpperCase", "..%2f..%2fetc", "@scope"} {
		resp = f.do(t, http.MethodGet, "/acme/npm/js/"+name, nil, true)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("name %q = %d, want 400", name, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestDeleteVersionRepointsLatest(t *testing.T) {
	f := newFixture(t)
	_, repoID := f.seedRepo("acme", "js", false)
	for _, v := range []string{"1.0.0", "1.2.0"} {
		resp := f.do(t, http.MethodPut, "/acme/npm/js/mini-lib", publishJSON(t, "mini-lib", v, []byte("tb-"+v)), true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("publish %s = %d", v, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// latest currently 1.2.0; delete it → latest re-points to 1.0.0.
	resp := f.do(t, http.MethodDelete, "/acme/npm/js/mini-lib/-/1.2.0", nil, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()

	tags, err := f.packages.DistTags(context.Background(), repoID, "mini-lib")
	if err != nil || tags["latest"] != "1.0.0" {
		t.Errorf("latest after delete = %q (err=%v)", tags["latest"], err)
	}
	// Deleted tarball 404s; row gone.
	resp = f.do(t, http.MethodGet, "/acme/npm/js/mini-lib/-/mini-lib-1.2.0.tgz", nil, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("tarball after delete = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if _, err := f.packages.FindByNameVersion(context.Background(), repoID, "mini-lib", "1.2.0"); err == nil {
		t.Errorf("row still present after delete")
	}
}

func TestAnonymousReadFollowsPublicRead(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("pub", "open", true)
	resp := f.do(t, http.MethodPut, "/pub/npm/open/mini-lib", publishJSON(t, "mini-lib", "1.0.0", []byte("tb")), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = f.do(t, http.MethodGet, "/pub/npm/open/mini-lib", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("anon public packument = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	f2 := newFixture(t)
	f2.seedRepo("priv", "closed", false)
	resp = f2.do(t, http.MethodPut, "/priv/npm/closed/mini-lib", publishJSON(t, "mini-lib", "1.0.0", []byte("tb")), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = f2.do(t, http.MethodGet, "/priv/npm/closed/mini-lib", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anon private packument = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestPing(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "js", false)
	resp := f.do(t, http.MethodGet, "/acme/npm/js/-/ping", nil, true)
	if body := mustBody(t, resp); resp.StatusCode != 200 || body != "{}" {
		t.Errorf("ping = %d %q", resp.StatusCode, body)
	}
}
