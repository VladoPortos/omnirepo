package helm_test

// Phase 5 Plan 05-05 (HELMRETRY-01 convergence + HELMRETRY-02 mid-flight abort
// regression) — end-to-end integration test that drives two back-to-back
// syncs against a 3-chart upstream fixture. The first sync stops with an
// upstream 500 on chart #3; the second sync (fixture healed) converges to
// the same row-set a clean first-run would have produced.
//
// This test is the HEADLINE proof for Phase 5: Plans 01-04 exercise the
// subsystems in isolation (typed error, MarkPermanentlyFailedWithLog, pool
// routing, boot-recovery ordering); Plan 05 is the single artifact that
// demonstrates the full chain — handler → PartialSyncErr → regen.Kick →
// FindByDigest cheap-skip — works end-to-end against a real upstream-
// shaped fixture.
//
// Scope boundary (per plan 05-05 execution_context):
//   - Does NOT call jobs.Pool.markFailed; the pool's terminal-failed
//     routing ships in Plan 03's unit tests. Here we assert ONLY the
//     handler's observable surface (typed error + helm_charts row set +
//     on-disk index.yaml).
//   - Uses a PARALLEL fixture (newHelmPartialSyncFixture) — NOT a
//     parameterised replacement — so the pre-existing 2-chart callers
//     (TestMirrorSync_Helm_Idempotent + TestHelmSync_EmitsStepProgress)
//     stay untouched (Pitfall 3 of the 05-CONTEXT).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	helmrepo "helm.sh/helm/v3/pkg/repo"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"

	"net/http"
	"net/http/httptest"
)

// partialSyncFixture bundles the values TestHelmSync_PartialThenConverges
// needs to drive the scenario and then assert convergence on disk + DB.
type partialSyncFixture struct {
	h           *helm.SyncHandler
	db          *metadata.DB
	repoID      int64
	upstreamURL string
	// failMode gates the chart-#3 handler. When true, /charts/postgres-...tgz
	// returns HTTP 500; tests flip it to false between syncs to exercise the
	// heal-then-converge path.
	failMode *atomic.Bool
	// Chart bytes captured from the fixture so assertions can compare
	// persisted digests against the real upstream-served bytes without
	// re-reading the httptest server.
	chart1, chart2, chart3 []byte
	// repoDir is <repoRoot>/<project>/helm/<repo> — the directory the
	// helm regen writes index.yaml into. Captured at fixture-build time
	// so assertions don't need to reach into fixture internals.
	repoDir string
	// registry is the live regen.Registry wired into SyncDeps.Coalescer
	// so sync-end Kick() actually runs helm.RegenFor and flushes
	// index.yaml to disk. ShutdownAll is registered via t.Cleanup.
	registry *regen.Registry
}

// newHelmPartialSyncFixture is the 3-chart variant of newHelmProgressFixture
// (Plan 05-05 Pitfall 3 — parallel fixture, not parameterised). Chart #3
// (postgres) is served by a handler that reads *failMode and returns HTTP
// 500 when true. Tests toggle failMode between syncs to drive the partial-
// then-converge scenario without any test-only hooks on SyncDeps.
//
// Unlike newHelmProgressFixture, this fixture wires a LIVE regen.Registry
// into SyncDeps.Coalescer (with helm.RegenFor as factory) so the sync-end
// Kick() actually rebuilds <repoDir>/index.yaml from the .tgz files on
// disk. The D-11 convergence assertion (index.yaml lists all 3 charts
// after the second sync) depends on this wiring.
// newHelmPartialSyncFixture builds the 3-chart + 500-on-#3 harness.
// maxParallel=1 is the serialized dispatch path; maxParallel>1 exercises
// the chart-dispatch goroutine loop with real concurrency (Codex Q5
// follow-up — locks filesAdded/mutex bookkeeping under parallel fetches).
func newHelmPartialSyncFixture(t *testing.T, maxParallel int) *partialSyncFixture {
	t.Helper()
	db := sqlitetest.New(t)

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)

	chart1 := makeChartTGZ(t, "nginx", "1.0.0", "1.25", "web server", nil)
	chart2 := makeChartTGZ(t, "redis", "7.0.0", "7.2", "key-value store", nil)
	chart3 := makeChartTGZ(t, "postgres", "15.0.0", "15", "rdbms", nil)
	d1 := shaHex(chart1)
	d2 := shaHex(chart2)
	d3 := shaHex(chart3)

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
  postgres:
    - apiVersion: v2
      name: postgres
      version: 15.0.0
      appVersion: "15"
      description: rdbms
      digest: ` + d3 + `
      urls:
        - charts/postgres-15.0.0.tgz
generated: "2026-04-20T00:00:00Z"
`

	var failMode atomic.Bool
	failMode.Store(true) // default: fail on chart #3

	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(index))
	})
	mux.HandleFunc("/charts/nginx-1.0.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(chart1)
	})
	mux.HandleFunc("/charts/redis-7.0.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(chart2)
	})
	mux.HandleFunc("/charts/postgres-15.0.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		if failMode.Load() {
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(chart3)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	const projName = "pp"
	const repoName = "r1"
	pid, err := projectsRepo.Create(ctx, projName, "phase5 plan 05 partial-then-converge")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "helm", repoName, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	repoDir := filepath.Join(repoRoot, projName, "helm", repoName)
	pathStore := storage.NewPathStore(repoRoot)

	// Audit logger — required by helm.RegenFor's success/failure audit emit.
	// Mirrors the pattern in regen_test.go::newRegenFixture.
	auditPath := filepath.Join(dataRoot, "logs", "audit.log")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
		t.Fatalf("mkdir audit: %v", err)
	}
	auditLogger, err := audit.New(db, auditPath, 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	// Live regen.Registry wired to helm.RegenFor. Short debounce (1ms) so
	// the sync-end Kick fires quickly; maxWait (100ms) keeps the hard cap
	// tight for the `-race -count=3` test budget.
	locks := storage.NewLocks()
	registry := regen.NewRegistry(1*time.Millisecond, 100*time.Millisecond,
		func(repoID int64) regen.RegenFn {
			return helm.RegenFor(helm.RegenDeps{
				DB:       db,
				Repos:    reposRepo,
				Projects: projectsRepo,
				Audit:    auditLogger,
				Locks:    locks,
				RepoRoot: repoRoot,
				RepoID:   repoID,
			})
		})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = registry.ShutdownAll(shutdownCtx)
	})

	h := helm.NewSyncHandler(helm.SyncDeps{
		DB:         db,
		Path:       pathStore,
		HelmCharts: helmCharts,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		Audit:      auditLogger,
		Coalescer:  registry,
		HTTPClient: srv.Client(),
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: maxParallel},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})

	return &partialSyncFixture{
		h:           h,
		db:          db,
		repoID:      rid,
		upstreamURL: srv.URL,
		failMode:    &failMode,
		chart1:      chart1,
		chart2:      chart2,
		chart3:      chart3,
		repoDir:     repoDir,
		registry:    registry,
	}
}

// waitForIndexYAML polls up to 2s for index.yaml at f.repoDir to exist.
// The coalescer is non-synchronous (debounce + background goroutine), so
// a short poll is the load-bearing wait. 2s is comfortably above the
// 1ms debounce + filesystem-write latency even under `-race` slowdown.
func (f *partialSyncFixture) waitForIndexYAML(t *testing.T) string {
	t.Helper()
	indexPath := filepath.Join(f.repoDir, "index.yaml")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(indexPath); err == nil {
			return indexPath
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("index.yaml never appeared at %s within 2s", indexPath)
	return ""
}

// TestHelmSync_PartialThenConverges is the combined HELMRETRY-01 +
// HELMRETRY-02 proof. See file-level doc comment for the scenario.
func TestHelmSync_PartialThenConverges(t *testing.T) {
	runPartialThenConverges(t, 1)
}

// TestHelmSync_PartialThenConverges_Parallel exercises the same scenario
// under MaxParallelDownloadsPerJob=4 so the chart-dispatch goroutine loop
// handles real concurrency alongside the partial-error return. Codex Q5
// follow-up: the serialized path alone doesn't prove the filesAdded /
// mu.Lock() bookkeeping is race-clean under parallel fetches.
func TestHelmSync_PartialThenConverges_Parallel(t *testing.T) {
	runPartialThenConverges(t, 4)
}

func runPartialThenConverges(t *testing.T, maxParallel int) {
	t.Helper()
	f := newHelmPartialSyncFixture(t, maxParallel)
	ctx := context.Background()
	helmCharts := metadata.NewHelmChartsRepo(f.db)

	payload := map[string]string{"upstream_url": f.upstreamURL}
	pb, _ := json.Marshal(payload)

	// ---------- First sync: fixture returns 500 on chart #3 ----------
	jobID1 := seedHelmSyncJobRow(t, f.db, f.repoID)
	err := f.h.Handle(ctx, string(pb), 0, f.repoID, jobID1)
	if err == nil {
		t.Fatalf("first sync: expected PartialSyncErr, got nil")
	}
	if !errors.Is(err, helm.ErrHelmPartialSync) {
		t.Fatalf("first sync: errors.Is(err, ErrHelmPartialSync) = false; err=%v", err)
	}
	var pse *helm.PartialSyncErr
	if !errors.As(err, &pse) {
		t.Fatalf("first sync: errors.As(err, *PartialSyncErr) = false; err=%v", err)
	}
	if pse.Persisted() != 2 || pse.Expected() != 3 {
		t.Fatalf("first sync: counts = (persisted=%d, expected=%d); want (2, 3)",
			pse.Persisted(), pse.Expected())
	}

	rows, rerr := helmCharts.ListByRepo(ctx, f.repoID)
	if rerr != nil {
		t.Fatalf("ListByRepo (first): %v", rerr)
	}
	if len(rows) != 2 {
		t.Fatalf("first sync: helm_charts rows = %d; want 2 (nginx+redis)", len(rows))
	}
	// Digests of the 2 persisted charts must match the fixture-served bytes —
	// proves real content landed, not placeholder rows.
	wantD1 := "sha256:" + shaHex(f.chart1)
	wantD2 := "sha256:" + shaHex(f.chart2)
	wantD3 := "sha256:" + shaHex(f.chart3)
	gotDigests := map[string]bool{}
	for _, r := range rows {
		gotDigests[r.Digest] = true
	}
	if !gotDigests[wantD1] {
		t.Errorf("first sync: nginx digest %q not in persisted set %v", wantD1, gotDigests)
	}
	if !gotDigests[wantD2] {
		t.Errorf("first sync: redis digest %q not in persisted set %v", wantD2, gotDigests)
	}
	if gotDigests[wantD3] {
		t.Errorf("first sync: postgres digest %q unexpectedly present after 500", wantD3)
	}

	// F-09.8 regen.Kick fired on partial — index.yaml exists on disk after
	// the first (failed) sync. Poll because the coalescer runs in its own
	// goroutine.
	firstIndexPath := f.waitForIndexYAML(t)
	firstIdx, lerr := helmrepo.LoadIndexFile(firstIndexPath)
	if lerr != nil {
		t.Fatalf("first sync: LoadIndexFile: %v", lerr)
	}
	for _, name := range []string{"nginx", "redis"} {
		if _, ok := firstIdx.Entries[name]; !ok {
			t.Errorf("first sync: index.yaml missing entry %q (entries=%v)",
				name, indexEntryNames(firstIdx))
		}
	}
	if _, ok := firstIdx.Entries["postgres"]; ok {
		t.Errorf("first sync: index.yaml unexpectedly has postgres entry")
	}

	// ---------- Heal the fixture ----------
	f.failMode.Store(false)

	// ---------- Second sync: converges to 3 charts ----------
	jobID2 := seedHelmSyncJobRow(t, f.db, f.repoID)
	if err := f.h.Handle(ctx, string(pb), 0, f.repoID, jobID2); err != nil {
		t.Fatalf("second sync: expected nil after heal, got: %v", err)
	}

	rows, rerr = helmCharts.ListByRepo(ctx, f.repoID)
	if rerr != nil {
		t.Fatalf("ListByRepo (second): %v", rerr)
	}
	if len(rows) != 3 {
		t.Fatalf("second sync: helm_charts rows = %d; want 3 (nginx+redis+postgres)", len(rows))
	}
	// Row set matches a clean first-run: all 3 digests present, names+versions
	// triple matches the fixture — D-11 convergence.
	got := map[string]string{} // name+version -> digest
	for _, r := range rows {
		got[r.Name+"@"+r.Version] = r.Digest
	}
	wantTriples := map[string]string{
		"nginx@1.0.0":     wantD1,
		"redis@7.0.0":     wantD2,
		"postgres@15.0.0": wantD3,
	}
	for k, wantDigest := range wantTriples {
		if got[k] != wantDigest {
			t.Errorf("second sync: chart %s digest = %q; want %q", k, got[k], wantDigest)
		}
	}

	// D-11 convergence on disk: index.yaml after the second sync lists all
	// 3 chart names. Poll again because the sync-end Kick is async.
	secondIndexPath := f.waitForIndexYAML(t)
	// Load the index and assert ALL 3 names are present. This is the
	// stronger form of the grep check in the plan acceptance criteria —
	// it uses the helm SDK's own parser so whitespace / key-order / YAML
	// quoting quirks can't produce false positives.
	//
	// Because the first-sync index.yaml already exists, poll for the
	// 3-entry version specifically — the coalescer could fire before
	// the second chart lands, leaving us observing a stale 2-entry file
	// transiently under `-race`. This loop retries the LoadIndexFile
	// until all 3 entries appear or the deadline expires.
	convergeDeadline := time.Now().Add(2 * time.Second)
	var secondIdx *helmrepo.IndexFile
	for time.Now().Before(convergeDeadline) {
		idx, lerr := helmrepo.LoadIndexFile(secondIndexPath)
		if lerr == nil && len(idx.Entries) == 3 {
			secondIdx = idx
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if secondIdx == nil {
		t.Fatalf("second sync: index.yaml never converged to 3 entries at %s", secondIndexPath)
	}
	for _, name := range []string{"nginx", "redis", "postgres"} {
		if _, ok := secondIdx.Entries[name]; !ok {
			t.Errorf("second sync: index.yaml missing entry %q (entries=%v)",
				name, indexEntryNames(secondIdx))
		}
	}

	// Belt-and-suspenders: grep the raw bytes for the 3 chart names so the
	// acceptance-criteria `grep -E '"(nginx|redis|postgres):"'` check
	// deterministically hits. helm SDK output uses `<name>:` key form.
	idxBytes, _ := os.ReadFile(secondIndexPath)
	for _, name := range []string{"nginx:", "redis:", "postgres:"} {
		if !strings.Contains(string(idxBytes), name) {
			t.Errorf("second sync: index.yaml raw bytes missing %q", name)
		}
	}
}

// indexEntryNames is a debug helper that returns the sorted slice of chart
// names in idx.Entries. Used in t.Errorf output so failures print the
// observed state rather than just "missing X".
func indexEntryNames(idx *helmrepo.IndexFile) []string {
	out := make([]string, 0, len(idx.Entries))
	for name := range idx.Entries {
		out = append(out, name)
	}
	return out
}
