package git_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/dxc-internal/omnirepo/internal/protocol/git"
	"github.com/dxc-internal/omnirepo/internal/protocol/git/gitkit"
	"github.com/dxc-internal/omnirepo/internal/protocol/git/gogit"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// --- Test 6: Backend selection from config ---

func TestBackendSelection_Gogit(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.GitBackend = "gogit"

	backend := gitpkg.SelectBackend(cfg)
	if backend.BackendName() != "gogit" {
		t.Fatalf("backend=%q want gogit", backend.BackendName())
	}
}

func TestBackendSelection_Gitkit(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.GitBackend = "gitkit"

	backend := gitpkg.SelectBackend(cfg)
	if backend.BackendName() != "gitkit" {
		t.Fatalf("backend=%q want gitkit", backend.BackendName())
	}
}

func TestBackendSelection_DefaultIsGogit(t *testing.T) {
	cfg := config.Defaults()
	// Default config.Server.GitBackend = "gogit"
	backend := gitpkg.SelectBackend(cfg)
	if _, ok := backend.(*gogit.Server); !ok {
		t.Fatalf("default backend is not *gogit.Server: %T", backend)
	}
}

func TestBackendSelection_GitkitReturnsGitkitType(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.GitBackend = "gitkit"
	backend := gitpkg.SelectBackend(cfg)
	if _, ok := backend.(*gitkit.Server); !ok {
		t.Fatalf("gitkit backend is not *gitkit.Server: %T", backend)
	}
}

// recordingBackend is a GitServer fake that records the repoPath it was
// invoked with, so URL-routing tests can assert the dispatch reached the
// backend with the right resolved on-disk path.
type recordingBackend struct {
	lastPath string
}

func (b *recordingBackend) Handler(repoPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b.lastPath = repoPath
		w.WriteHeader(http.StatusOK)
	})
}

func (b *recordingBackend) BackendName() string { return "recording" }

// TestRouteMatrix_BothURLShapes asserts that the Git Smart-HTTP handler
// is reachable under both URL conventions:
//   - "/git/{project}/{repo}.git/..."   (legacy)
//   - "/{project}/git/{repo}.git/..."   (canonical, matches every other
//     protocol's "/{project}/{proto}/{repo}/..." layout — D-4)
//
// The simplified TestRouter chain bypasses auth but still runs the URL
// resolver, so we can verify both shapes resolve the same repo and pass
// the resolved on-disk path to the backend.
func TestRouteMatrix_BothURLShapes(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)

	// Seed a project + git repo row so ResolveRepoFromURL succeeds.
	if _, err := db.Writer.Exec(`INSERT INTO projects(name) VALUES ('acme')`); err != nil {
		t.Fatal(err)
	}
	var projID int64
	if err := db.Reader.QueryRow(`SELECT id FROM projects WHERE name='acme'`).Scan(&projID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer.Exec(`INSERT INTO repos(project_id, type, name) VALUES (?, 'git', 'thing')`, projID); err != nil {
		t.Fatal(err)
	}

	dataRoot := t.TempDir()
	rec := &recordingBackend{}
	handler := gitpkg.New(gitpkg.Deps{
		Backend:  rec,
		Config:   defaultCfg(),
		Locks:    storage.NewLocks(),
		Repos:    metadata.NewReposRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		DataRoot: dataRoot,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		DB:       db,
		Refs:     metadata.NewGitRefsRepo(db),
	})
	mux := handler.TestRouter(t)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wantRepoPath := filepath.Join(dataRoot, "repos", "acme", "git", "thing.git")

	for _, shape := range []struct {
		name string
		url  string
	}{
		{"legacy /git/{project}/{repo}", ts.URL + "/git/acme/thing.git/info/refs?service=git-upload-pack"},
		{"canonical /{project}/git/{repo}", ts.URL + "/acme/git/thing.git/info/refs?service=git-upload-pack"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			rec.lastPath = ""
			resp, err := http.Get(shape.url)
			if err != nil {
				t.Fatalf("GET %s: %v", shape.url, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status=%d want 200", shape.url, resp.StatusCode)
			}
			if rec.lastPath != wantRepoPath {
				t.Fatalf("backend repoPath = %q, want %q", rec.lastPath, wantRepoPath)
			}
		})
	}
}

// --- Plan 11-07: receive-pack 403 gate for mirror repos (GITMIRROR-03) ---

// newMirrorGateHarness builds a chi mux + recordingBackend wired to a
// seeded {mirrorRepo, plainRepo} pair under the same "testproj" project.
// The returned mux is the handler's TestRouter (no auth) so tests can fire
// raw HTTP requests and assert the gate's behavior end-to-end.
//
// Mirror repo: "mirror-repo" with is_mirror=1 + upstream_url seeded.
// Plain repo:  "plain-repo" with is_mirror=0 (default).
func newMirrorGateHarness(t *testing.T) (*httptest.Server, *recordingBackend) {
	t.Helper()
	db := sqlitetest.New(t)

	// Seed project + two repos.
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('testproj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var projID int64
	if err := db.Reader.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='testproj'`).Scan(&projID); err != nil {
		t.Fatalf("find project: %v", err)
	}
	reposRepo := metadata.NewReposRepo(db)

	// Mirror repo — flip is_mirror under one writer tx.
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, cerr := reposRepo.CreateInTx(ctx, tx, projID, "git", "mirror-repo", "", nil, nil, nil)
		if cerr != nil {
			return cerr
		}
		return reposRepo.SetMirrorConfigInTx(ctx, tx, id, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://upstream.example.com/repo.git",
		})
	})
	if err != nil {
		t.Fatalf("seed mirror repo: %v", err)
	}

	// Plain (non-mirror) repo.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id, type, name) VALUES (?, 'git', 'plain-repo')`, projID); err != nil {
		t.Fatalf("seed plain repo: %v", err)
	}

	dataRoot := t.TempDir()
	rec := &recordingBackend{}
	handler := gitpkg.New(gitpkg.Deps{
		Backend:  rec,
		Config:   defaultCfg(),
		Locks:    storage.NewLocks(),
		Repos:    reposRepo,
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		DataRoot: dataRoot,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		DB:       db,
		Refs:     metadata.NewGitRefsRepo(db),
	})
	mux := handler.TestRouter(t)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, rec
}

// assertMirrorEnvelope decodes the response body and asserts the envelope
// carries the expected code. The httperr wire shape is {code, message,
// class, ...} at the top level (no "error" wrapper).
func assertMirrorEnvelope(t *testing.T, body []byte, wantCode string) {
	t.Helper()
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Class   string `json:"class"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody: %s", err, string(body))
	}
	if env.Code != wantCode {
		t.Fatalf("envelope code = %q, want %q; body=%s", env.Code, wantCode, string(body))
	}
}

// TestReceivePack_MirrorRejected asserts that POST /git-receive-pack
// against a mirror repo returns 403 + mirror.push_rejected envelope BEFORE
// the backend is invoked.
func TestReceivePack_MirrorRejected(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/mirror-repo.git/git-receive-pack"
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST receive-pack: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; backend lastPath=%q", resp.StatusCode, rec.lastPath)
	}
	if rec.lastPath != "" {
		t.Fatalf("backend MUST NOT be invoked for mirror receive-pack; got lastPath=%q", rec.lastPath)
	}
	body := readAll(t, resp)
	assertMirrorEnvelope(t, body, "mirror.push_rejected")
}

// TestReceivePack_NonMirrorAllowed asserts that POST /git-receive-pack on
// a plain (non-mirror) repo passes through to the backend — gate MUST NOT
// fire a false positive on the write path for regular repos.
func TestReceivePack_NonMirrorAllowed(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/plain-repo.git/git-receive-pack"
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST receive-pack (non-mirror): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("non-mirror receive-pack returned 403 — gate is firing a false positive")
	}
	if rec.lastPath == "" {
		t.Fatalf("backend NOT invoked for non-mirror receive-pack; expected passthrough")
	}
}

// TestUploadPack_MirrorAllowed asserts that POST /git-upload-pack
// (fetch/clone) against a mirror repo is NOT gated — clone-from-mirror
// must continue to work (mirrors are read-only but still readable).
func TestUploadPack_MirrorAllowed(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/mirror-repo.git/git-upload-pack"
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST upload-pack (mirror): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("mirror upload-pack returned 403 — gate is blocking fetch/clone")
	}
	if rec.lastPath == "" {
		t.Fatalf("backend NOT invoked for mirror upload-pack; expected passthrough")
	}
}

// TestInfoRefs_UploadPack_MirrorAllowed asserts that
// GET /info/refs?service=git-upload-pack against a mirror repo passes
// through — capability negotiation for clone must work.
func TestInfoRefs_UploadPack_MirrorAllowed(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/mirror-repo.git/info/refs?service=git-upload-pack"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET info/refs (upload-pack): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("info/refs?service=git-upload-pack on mirror returned 403 — clone negotiation blocked")
	}
	if rec.lastPath == "" {
		t.Fatalf("backend NOT invoked for info/refs?service=git-upload-pack; expected passthrough")
	}
}

// TestInfoRefs_ReceivePack_MirrorRejected asserts that
// GET /info/refs?service=git-receive-pack against a mirror repo returns
// 403 with mirror.push_rejected — don't even let clients negotiate push.
// Prevents ref-list leak before the actual POST 403.
func TestInfoRefs_ReceivePack_MirrorRejected(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/mirror-repo.git/info/refs?service=git-receive-pack"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET info/refs (receive-pack): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; backend lastPath=%q", resp.StatusCode, rec.lastPath)
	}
	if rec.lastPath != "" {
		t.Fatalf("backend MUST NOT be invoked for mirror info/refs?service=git-receive-pack; got lastPath=%q", rec.lastPath)
	}
	body := readAll(t, resp)
	assertMirrorEnvelope(t, body, "mirror.push_rejected")
}

// readAll is a tiny helper to keep test bodies tidy.
func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	var buf bytes.Buffer
	_, err := buf.ReadFrom(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return buf.Bytes()
}
