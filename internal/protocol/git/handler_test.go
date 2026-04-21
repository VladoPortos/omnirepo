package git_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
