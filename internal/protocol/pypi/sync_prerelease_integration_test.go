package pypi_test

// Regression guards for the mirror sync path.
//
// First guard: a sdist filename with a dashed pre-release suffix
// (`widget-1.0.0-rc1.tar.gz`) must land in pypi_files with
// version="1.0.0-rc1", not version="rc1". The pre-fix inline parser used
// LastIndex("-") and attributed the tail segment to the version column.
//
// Second guard: a hostile upstream filename (path separator / control
// chars / header-injection bytes) must be rejected at ingest so PathStore
// never sees it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/pypi"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// newPyPIPreleaseFixture serves a single project with three files:
//   - widget-1.0.0-rc1.tar.gz — dashed pre-release regression guard
//   - widget-1.0.0.tar.gz     — canonical control case
//   - widget/../escape.tar.gz — allowlist guard (must be dropped
//     without ever hitting the package endpoint because the collect-pass
//     filter rejects the filename)
func newPyPIPreleaseFixture(t *testing.T) (*pypi.SyncHandler, *metadata.DB, int64, string) {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	pypiFiles := metadata.NewPyPIFilesRepo(db)
	scans := metadata.NewScansRepo(db)

	bodyPre := []byte("sdist-bytes-rc1")
	bodyRel := []byte("sdist-bytes-release")
	dPre := sha256Hex(bodyPre)
	dRel := sha256Hex(bodyRel)

	mux := http.NewServeMux()
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(`{"meta":{"api-version":"1.0"},"projects":[{"name":"widget"}]}`))
	})
	// The hostile filename url field points at /packages/escape — if the
	// allowlist regresses we'd see a pypi_files row insert; if the
	// allowlist holds, the collect-pass filter drops the entry before any
	// download is attempted.
	projectJSON := fmt.Sprintf(`{
		"meta": {"api-version":"1.0"},
		"name":"widget",
		"files":[
			{"filename":"widget-1.0.0-rc1.tar.gz","url":"/packages/widget-1.0.0-rc1.tar.gz","hashes":{"sha256":"%s"},"size":%d},
			{"filename":"widget-1.0.0.tar.gz","url":"/packages/widget-1.0.0.tar.gz","hashes":{"sha256":"%s"},"size":%d},
			{"filename":"../etc/passwd.tar.gz","url":"/packages/escape.tar.gz","hashes":{"sha256":"%s"},"size":4}
		]
	}`, dPre, len(bodyPre), dRel, len(bodyRel), dPre)
	mux.HandleFunc("/simple/widget/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(projectJSON))
	})
	mux.HandleFunc("/packages/widget-1.0.0-rc1.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bodyPre)
	})
	mux.HandleFunc("/packages/widget-1.0.0.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bodyRel)
	})
	mux.HandleFunc("/packages/escape.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("hostile filename reached download — allowlist regressed")
		_, _ = w.Write([]byte("evil"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "pre", "pypi pre-release regression fixture")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "pypi", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)

	h := pypi.NewSyncHandler(pypi.SyncDeps{
		DB:         db,
		Path:       pathStore,
		PyPIFiles:  pypiFiles,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		HTTPClient: srv.Client(),
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})
	return h, db, rid, srv.URL
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestMirrorSync_PyPI_DashedPrereleaseVersion locks the sync-handler reroute
// through parseSdistFilename so `widget-1.0.0-rc1.tar.gz` stores
// version=`1.0.0-rc1` (not `rc1`).
func TestMirrorSync_PyPI_DashedPrereleaseVersion(t *testing.T) {
	h, db, repoID, upURL := newPyPIPreleaseFixture(t)
	ctx := context.Background()

	jobID := seedPyPISyncJob(t, db, repoID)
	payload := map[string]any{"upstream_url": upURL}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	pypiFiles := metadata.NewPyPIFilesRepo(db)
	rows, err := pypiFiles.ListByProject(ctx, repoID, "widget")
	if err != nil {
		t.Fatalf("list widget files: %v", err)
	}
	// Two canonical files + hostile filename dropped by allowlist.
	if len(rows) != 2 {
		t.Fatalf("got %d pypi_files rows; want 2 (rc1 + release, hostile dropped)", len(rows))
	}

	want := map[string]string{
		"widget-1.0.0-rc1.tar.gz": "1.0.0-rc1",
		"widget-1.0.0.tar.gz":     "1.0.0",
	}
	for _, r := range rows {
		exp, ok := want[r.Filename]
		if !ok {
			t.Errorf("unexpected filename %q in pypi_files", r.Filename)
			continue
		}
		if r.Version != exp {
			t.Errorf("pypi_files row %q: version=%q, want %q", r.Filename, r.Version, exp)
		}
	}
}

// TestMirrorSync_PyPI_HostileFilenameRejected verifies the collect-pass
// allowlist must drop upstream entries whose filename contains path
// separators, control chars, or other header-injection shapes BEFORE any
// download is attempted. Fixture's hostile handler t.Errorf's on any hit.
func TestMirrorSync_PyPI_HostileFilenameRejected(t *testing.T) {
	h, db, repoID, upURL := newPyPIPreleaseFixture(t)
	ctx := context.Background()

	jobID := seedPyPISyncJob(t, db, repoID)
	payload := map[string]any{"upstream_url": upURL}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Confirm pypi_files has exactly the two canonical rows — the hostile
	// filename must have no row. ListByProject is keyed on normalized
	// project name; the hostile filename does not reach Insert at all.
	pypiFiles := metadata.NewPyPIFilesRepo(db)
	rows, err := pypiFiles.ListByProject(ctx, repoID, "widget")
	if err != nil {
		t.Fatalf("list widget files: %v", err)
	}
	for _, r := range rows {
		if r.Filename == "../etc/passwd.tar.gz" {
			t.Fatalf("hostile filename landed in pypi_files: %+v", r)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("got %d pypi_files rows; want 2 canonical", len(rows))
	}
}
