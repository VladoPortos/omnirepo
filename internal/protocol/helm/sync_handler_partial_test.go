package helm_test

// Phase 5 Plan 01 HELMRETRY-03 — ctx-cancel partial-sync integration test.
//
// Lives in package helm_test (external) so it can reuse the chart-fixture
// builders from sync_progress_test.go / testutil_test.go. The Details-
// shape audit test lives in sync_handler_internal_test.go (package helm)
// because it needs the unexported newPartialSyncErr constructor +
// unexported fail() method.
//
// Triggers ctx cancellation AFTER ParseUpstream succeeds but BEFORE the
// dispatch loop finishes all N charts. Uses a chart-serving handler that
// cancels ctx on the first chart GET: the first goroutine either commits
// or observes the cancellation mid-flight; the loop's ctx.Err() gate
// then catches the second iteration and breaks without dispatching.
//
// Pre-cancelling ctx before Handle would trip the FindByID / ParseUpstream
// calls that run BEFORE the dispatch loop, so the handler would error out
// through a path that never reaches newPartialSyncErr. Cancelling mid-
// dispatch is the precise race the D-03a contract covers.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/helm"
	"github.com/vladoportos/omnirepo/internal/storage"
)

func TestHelmSync_CtxCancel_ReturnsPartial(t *testing.T) {
	db := sqlitetest.New(t)

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)

	chart1 := makeChartTGZ(t, "nginx", "1.0.0", "1.25", "web server", nil)
	chart2 := makeChartTGZ(t, "redis", "7.0.0", "7.2", "key-value store", nil)
	d1 := sha256Hex(chart1)
	d2 := sha256Hex(chart2)

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

	// cancel is populated below once the ctx is created.
	var (
		cancelFn    context.CancelFunc
		firstHit    sync.Once
		chartHits   atomic.Int32
		chartHitsMu sync.Mutex
	)

	// chartHandler is shared by both chart endpoints so the test does not
	// depend on YAML-map iteration order (index.yaml entries may parse as
	// either [nginx, redis] or [redis, nginx] — Go map iteration is
	// deliberately non-deterministic under -race). The first chart
	// request triggers cancel(); both handlers return their bytes.
	chartHandler := func(body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			chartHitsMu.Lock()
			chartHits.Add(1)
			chartHitsMu.Unlock()
			firstHit.Do(func() {
				if cancelFn != nil {
					cancelFn()
				}
				// Let the cancel propagate past the sem-send gate in
				// the dispatch loop before this response unblocks the
				// caller-side sem release.
				time.Sleep(25 * time.Millisecond)
			})
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(body)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(index))
	})
	mux.HandleFunc("/charts/nginx-1.0.0.tgz", chartHandler(chart1))
	mux.HandleFunc("/charts/redis-7.0.0.tgz", chartHandler(chart2))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancelFn = cancel
	t.Cleanup(cancel)

	pid, err := projectsRepo.Create(ctx, "pp", "ctx-cancel partial test")
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

	// Seed a sync_jobs row so Handle's ProgressWriter has a job id.
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('helm_sync', ?, 'running', '{}', '{}')`,
		rid,
	)
	if err != nil {
		t.Fatalf("seed sync_jobs row: %v", err)
	}
	jobID, _ := res.LastInsertId()

	payload := map[string]string{"upstream_url": srv.URL}
	pb, _ := json.Marshal(payload)

	herr := h.Handle(ctx, string(pb), 0, rid, jobID)
	if herr == nil {
		t.Fatalf("Handle returned nil; want partial-sync error after mid-dispatch cancel")
	}
	if !errors.Is(herr, helm.ErrHelmPartialSync) {
		t.Fatalf("errors.Is(err, ErrHelmPartialSync) = false; got err=%v", herr)
	}
	var pse *helm.PartialSyncErr
	if !errors.As(herr, &pse) {
		t.Fatalf("errors.As(err, &pse) = false; got err=%v", herr)
	}
	if pse.Expected() != 2 {
		t.Errorf("Expected() = %d; want 2 (two charts in fixture index.yaml)", pse.Expected())
	}
	if p := pse.Persisted(); p < 0 || p > 2 {
		t.Errorf("Persisted() = %d; want in [0,2] (bounded by fixture chart count)", p)
	}
	// The dispatch loop's ctx.Err() gate must break before dispatching
	// chart 2. Exactly one chart handler should have fired (the one that
	// triggered the cancel via firstHit.Once). More than one = gate
	// regression (acceptance criterion — HELMRETRY-03 Pitfall 1).
	if n := chartHits.Load(); n != 1 {
		t.Errorf("chart handler hits = %d; want 1 (ctx.Err gate must break before chart 2 dispatch)", n)
	}
}

// sha256Hex mirrors shaHex from sync_progress_test.go. Duplicated here so
// this file does not collide with the _test.go helper's unexported name;
// Go treats _test.go files in the same package as one compilation unit
// so re-declaring shaHex would be a duplicate-symbol error.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
