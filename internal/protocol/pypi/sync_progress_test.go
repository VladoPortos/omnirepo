package pypi_test

// Phase 8 Plan 02 / M2.6 — byte-level progress emission for PyPI sync.
//
// Pre-computes totalBytes from summed PEP 691 file.size entries (D-11),
// wraps per-file downloads with jobs.CountingReader. Test serves a fake
// Simple index + two .whl-shaped files and verifies the sync_jobs row
// carries advanced progress_bytes + a "done" or "pulling <filename>"
// step after sync completes.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

func pypiShaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newPyPIProgressFixture(t *testing.T) (*pypi.SyncHandler, *metadata.DB, int64, string) {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	pypiFiles := metadata.NewPyPIFilesRepo(db)
	scans := metadata.NewScansRepo(db)

	// Two "wheel" payloads — content doesn't have to parse as a valid
	// zip; the sync handler only stores bytes + inserts pypi_files row.
	body1 := []byte("not-really-a-wheel-" + strings.Repeat("x", 200))
	body2 := []byte("also-not-really-a-wheel-" + strings.Repeat("y", 300))
	d1 := pypiShaHex(body1)
	d2 := pypiShaHex(body2)

	mux := http.NewServeMux()

	// PEP 691 index at /simple/ returns JSON listing available projects.
	indexJSON := `{"meta":{"api-version":"1.0"},"projects":[{"name":"acme"}]}`
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(indexJSON))
	})
	// PEP 691 per-project at /simple/acme/.
	projectJSON := fmt.Sprintf(`{
		"meta": {"api-version":"1.0"},
		"name":"acme",
		"files":[
			{"filename":"acme-1.0.0-py3-none-any.whl","url":"/packages/acme-1.0.0-py3-none-any.whl","hashes":{"sha256":"%s"},"size":%d},
			{"filename":"acme-1.1.0-py3-none-any.whl","url":"/packages/acme-1.1.0-py3-none-any.whl","hashes":{"sha256":"%s"},"size":%d}
		]
	}`, d1, len(body1), d2, len(body2))
	mux.HandleFunc("/simple/acme/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(projectJSON))
	})
	mux.HandleFunc("/packages/acme-1.0.0-py3-none-any.whl", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body1)
	})
	mux.HandleFunc("/packages/acme-1.1.0-py3-none-any.whl", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body2)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "pp", "phase8 plan 02 pypi")
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

func seedPyPISyncJob(t *testing.T, db *metadata.DB, repoID int64) int64 {
	t.Helper()
	res, err := db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('pypi_sync', ?, 'running', '{}', '{}')`,
		repoID,
	)
	if err != nil {
		t.Fatalf("seed sync_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestPyPISync_EmitsByteProgress(t *testing.T) {
	h, db, repoID, upURL := newPyPIProgressFixture(t)
	jobID := seedPyPISyncJob(t, db, repoID)

	payload := map[string]any{"upstream_url": upURL}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(context.Background(), string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var (
		progressBytes, totalBytes int64
		currentStep               string
	)
	if err := db.Reader.QueryRowContext(context.Background(),
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&progressBytes, &totalBytes, &currentStep); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if totalBytes <= 0 {
		t.Errorf("total_bytes=%d; want >0 (sum of PEP 691 file.size)", totalBytes)
	}
	if progressBytes <= 0 {
		t.Errorf("progress_bytes=%d; want >0", progressBytes)
	}
	if currentStep != "done" && !strings.HasPrefix(currentStep, "pulling ") {
		t.Errorf("current_step=%q; want 'done' or 'pulling <filename>'", currentStep)
	}
}
