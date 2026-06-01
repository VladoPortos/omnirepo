package pypi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/pypi"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// regenFixture provides DB + audit + repo on disk for regen tests.
type regenFixture struct {
	t        *testing.T
	db       *metadata.DB
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	pypiRepo *metadata.PyPIFilesRepo
	auditLog audit.Logger
	repoRoot string
	repoID   int64
	projID   int64
}

func newRegenFixture(t *testing.T) *regenFixture {
	t.Helper()
	db := sqlitetest.New(t)
	projects := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)
	pypiRepo := metadata.NewPyPIFilesRepo(db)

	pid, err := projects.Create(context.Background(), "proj1", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	publicRead := false
	autoScan := false
	rid, err := repos.Create(context.Background(), pid, "pypi", "internal", "", &autoScan, nil, &publicRead)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	repoRoot := t.TempDir()
	auditPath := filepath.Join(repoRoot, "audit.log")
	auditLog, err := audit.New(db, auditPath, 1, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	return &regenFixture{
		t:        t,
		db:       db,
		repos:    repos,
		projects: projects,
		pypiRepo: pypiRepo,
		auditLog: auditLog,
		repoRoot: repoRoot,
		repoID:   rid,
		projID:   pid,
	}
}

func (f *regenFixture) insertFile(t *testing.T, projNorm, version, filename, kind, requiresPython, digest string) {
	t.Helper()
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := f.pypiRepo.Insert(context.Background(), tx, &metadata.PyPIFile{
			RepoID:            f.repoID,
			ProjectNormalized: projNorm,
			Version:           version,
			Filename:          filename,
			Kind:              kind,
			RequiresPython:    requiresPython,
			SizeBytes:         100,
			Digest:            digest,
			CoreMetadataJSON:  "{}",
		})
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestPyPIRegenEmpty(t *testing.T) {
	f := newRegenFixture(t)
	fn := pypi.RegenFor(pypi.RegenDeps{
		DB:        f.db,
		Repos:     f.repos,
		Projects:  f.projects,
		PyPIFiles: f.pypiRepo,
		Audit:     f.auditLog,
		Locks:     storage.NewLocks(),
		RepoRoot:  f.repoRoot,
		RepoID:    f.repoID,
	})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	indexPath := filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "simple", "index.html")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(body), "Simple index") {
		t.Fatalf("body missing title: %s", body)
	}
	jsonPath := filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "simple", "index.json")
	jbody, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var doc struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
		Projects []any `json:"projects"`
	}
	if err := json.Unmarshal(jbody, &doc); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, jbody)
	}
	if doc.Meta.APIVersion != "1.0" {
		t.Fatalf("api-version=%q", doc.Meta.APIVersion)
	}
	if len(doc.Projects) != 0 {
		t.Fatalf("projects: %+v", doc.Projects)
	}

	state, _, err := f.repos.GetMetadataState(context.Background(), f.repoID)
	if err != nil {
		t.Fatalf("GetMetadataState: %v", err)
	}
	if state != metadata.MetadataStateClean {
		t.Fatalf("state=%q want clean", state)
	}
}

func TestPyPIRegenWithProjects(t *testing.T) {
	f := newRegenFixture(t)
	f.insertFile(t, "flask", "2.3.0", "flask-2.3.0-py3-none-any.whl", "wheel", ">=3.8",
		"sha256:deadbeef00000000000000000000000000000000000000000000000000000000")
	f.insertFile(t, "flask", "2.3.0", "flask-2.3.0.tar.gz", "sdist", ">=3.8",
		"sha256:cafebabe00000000000000000000000000000000000000000000000000000000")
	f.insertFile(t, "zope-interface", "5.5.2", "zope.interface-5.5.2.tar.gz", "sdist", ">=2.7",
		"sha256:abcd0000000000000000000000000000000000000000000000000000000000aa")

	fn := pypi.RegenFor(pypi.RegenDeps{
		DB:        f.db,
		Repos:     f.repos,
		Projects:  f.projects,
		PyPIFiles: f.pypiRepo,
		Audit:     f.auditLog,
		Locks:     storage.NewLocks(),
		RepoRoot:  f.repoRoot,
		RepoID:    f.repoID,
	})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}

	// Top-level lists both projects.
	idx, err := os.ReadFile(filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "simple", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(idx), `<a href="flask/">flask</a>`) {
		t.Fatalf("missing flask anchor: %s", idx)
	}
	if !strings.Contains(string(idx), `<a href="zope-interface/">zope-interface</a>`) {
		t.Fatalf("missing zope anchor: %s", idx)
	}

	// Per-project /flask/index.html with sha256= fragments.
	flaskHTML, err := os.ReadFile(filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "simple", "flask", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(flaskHTML)
	if !strings.Contains(body, "#sha256=deadbeef") {
		t.Fatalf("missing wheel sha: %s", body)
	}
	if !strings.Contains(body, "#sha256=cafebabe") {
		t.Fatalf("missing sdist sha: %s", body)
	}
	if !strings.Contains(body, `data-requires-python="&gt;=3.8"`) {
		t.Fatalf("missing data-requires-python: %s", body)
	}

	// JSON parses as PEP 691.
	flaskJSON, err := os.ReadFile(filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "simple", "flask", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
		Name  string `json:"name"`
		Files []struct {
			Filename string            `json:"filename"`
			URL      string            `json:"url"`
			Hashes   map[string]string `json:"hashes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(flaskJSON, &doc); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, flaskJSON)
	}
	if doc.Meta.APIVersion != "1.0" {
		t.Fatalf("api-version=%q", doc.Meta.APIVersion)
	}
	if doc.Name != "flask" {
		t.Fatalf("name=%q", doc.Name)
	}
	if len(doc.Files) != 2 {
		t.Fatalf("files=%d want 2", len(doc.Files))
	}
}

func TestPyPIRegenContentHashNames(t *testing.T) {
	f := newRegenFixture(t)
	f.insertFile(t, "flask", "1.0.0", "flask-1.0.0-py3-none-any.whl", "wheel", "",
		"sha256:1111111111111111111111111111111111111111111111111111111111111111")

	fn := pypi.RegenFor(pypi.RegenDeps{
		DB:        f.db,
		Repos:     f.repos,
		Projects:  f.projects,
		PyPIFiles: f.pypiRepo,
		Audit:     f.auditLog,
		Locks:     storage.NewLocks(),
		RepoRoot:  f.repoRoot,
		RepoID:    f.repoID,
	})
	// Two regens with same content → exactly one hashed file remains per
	// directory (sweepStale invariant).
	if err := fn(context.Background()); err != nil {
		t.Fatalf("regen 1: %v", err)
	}
	if err := fn(context.Background()); err != nil {
		t.Fatalf("regen 2: %v", err)
	}
	simpleDir := filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "simple")
	matches, _ := filepath.Glob(filepath.Join(simpleDir, "index-*.html"))
	if len(matches) != 1 {
		t.Fatalf("top index-*.html count=%d want 1: %v", len(matches), matches)
	}
	matches, _ = filepath.Glob(filepath.Join(simpleDir, "flask", "index-*.html"))
	if len(matches) != 1 {
		t.Fatalf("flask index-*.html count=%d want 1: %v", len(matches), matches)
	}
}

func TestPyPIRegenFailureMarksDirty(t *testing.T) {
	f := newRegenFixture(t)
	// Replace the repo root with a *file* so MkdirAll fails.
	bad := filepath.Join(f.repoRoot, "barrier")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the simple dir creation under the barrier path.
	fn := pypi.RegenFor(pypi.RegenDeps{
		DB:        f.db,
		Repos:     f.repos,
		Projects:  f.projects,
		PyPIFiles: f.pypiRepo,
		Audit:     f.auditLog,
		Locks:     storage.NewLocks(),
		RepoRoot:  bad, // file, not dir
		RepoID:    f.repoID,
	})
	if err := fn(context.Background()); err == nil {
		t.Fatal("expected regen to fail")
	}
	state, lastErr, err := f.repos.GetMetadataState(context.Background(), f.repoID)
	if err != nil {
		t.Fatalf("GetMetadataState: %v", err)
	}
	if state != metadata.MetadataStateDirty {
		t.Fatalf("state=%q want dirty", state)
	}
	if lastErr == "" {
		t.Fatal("last_regen_error empty after failure")
	}
	// Avoid time-pkg unused.
	_ = time.Now
}
