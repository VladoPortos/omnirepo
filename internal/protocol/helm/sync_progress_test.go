package helm_test

// Phase 8 Plan 02 / M2.7 — step-based progress emission.
//
// Helm is the only protocol that is step-based per D-11 because
// index.yaml does not carry per-chart sizes. After a sync, the
// sync_jobs row should carry:
//   - total_bytes == 0           (D-11: Helm is step-based)
//   - progress_bytes == N_charts (1-based completed chart count)
//   - current_step regex matches /chart \d+ of \d+ · .*\.tgz/ OR is "done"

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// shaHex is a tiny helper that computes the sha256 hex digest of b. Used
// to populate the `digest:` field in the fake upstream index.yaml so the
// handler's per-chart digest check passes.
func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newHelmProgressFixture sets up a real DB + PathStore + registered
// project/repo + a fake upstream that serves index.yaml AND the per-chart
// .tgz files referenced from it. Returns the SyncHandler, DB, repoID, and
// upstream URL.
func newHelmProgressFixture(t *testing.T) (*helm.SyncHandler, *metadata.DB, int64, string) {
	t.Helper()
	db := sqlitetest.New(t)

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)

	chart1 := makeChartTGZ(t, "nginx", "1.0.0", "1.25", "web server", nil)
	chart2 := makeChartTGZ(t, "redis", "7.0.0", "7.2", "key-value store", nil)
	d1 := shaHex(chart1)
	d2 := shaHex(chart2)

	index := `apiVersion: v1
entries:
  nginx:
    - apiVersion: v2
      name: nginx
      version: 1.0.0
      appVersion: "1.25"
      description: web server
      digest: ` + d1 + `
      urls:
        - charts/nginx-1.0.0.tgz
  redis:
    - apiVersion: v2
      name: redis
      version: 7.0.0
      appVersion: "7.2"
      description: key-value store
      digest: ` + d2 + `
      urls:
        - charts/redis-7.0.0.tgz
generated: "2026-04-20T00:00:00Z"
`
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(index))
	})
	mux.HandleFunc("/charts/nginx-1.0.0.tgz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(chart1)
	})
	mux.HandleFunc("/charts/redis-7.0.0.tgz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(chart2)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	pid, err := projectsRepo.Create(ctx, "pp", "phase8 plan 02 helm")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "helm", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)

	h := helm.NewSyncHandler(helm.SyncDeps{
		DB:         db,
		Path:       pathStore,
		HelmCharts: helmCharts,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		Coalescer:  nil,
		HTTPClient: srv.Client(),
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})
	return h, db, rid, srv.URL
}

func seedHelmSyncJobRow(t *testing.T, db *metadata.DB, repoID int64) int64 {
	t.Helper()
	res, err := db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('helm_sync', ?, 'running', '{}', '{}')`,
		repoID,
	)
	if err != nil {
		t.Fatalf("seed sync_jobs row: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestHelmSync_EmitsStepProgress(t *testing.T) {
	h, db, repoID, upURL := newHelmProgressFixture(t)
	jobID := seedHelmSyncJobRow(t, db, repoID)

	payload := map[string]string{"upstream_url": upURL}
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
		t.Fatalf("scan sync_jobs row: %v", err)
	}

	if totalBytes != 0 {
		t.Errorf("total_bytes=%d; want 0 (Helm is step-based per D-11)", totalBytes)
	}
	if progressBytes < 1 {
		t.Errorf("progress_bytes=%d; want >=1 (at least one chart emit)", progressBytes)
	}
	re := regexp.MustCompile(`^(done|chart \d+ of \d+ · .+\.tgz)$`)
	if !re.MatchString(currentStep) {
		t.Errorf("current_step=%q; want match %s", currentStep, re.String())
	}
	if currentStep == "done" && progressBytes != 2 {
		t.Errorf("done progress_bytes=%d; want 2 for 2-chart upstream", progressBytes)
	}
}
