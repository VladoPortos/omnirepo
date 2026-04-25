package helm_test

// v1.5 Phase 6 Plan 06-07 (DRIFTPURGE-01..05) — Helm mirror drift-purge
// integration test.
//
// Seed 3 helm_charts rows directly, then run a sync whose upstream
// index.yaml publishes only 2 of the 3 charts. The drift step detects
// the 3rd chart and purges it via Trash.MoveWithSnapshot. Both the
// HTTP path (tested here) and the OCI path land in the same
// helm_charts table per D-14; drift logic is identical for both.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

type helmDriftFixture struct {
	h           *helm.SyncHandler
	db          *metadata.DB
	repoID      int64
	upstreamURL string
	trashRoot   string
	dataRoot    string
	indexBody   string
}

func (f *helmDriftFixture) setIndex(body string) {
	f.indexBody = body
}

func newHelmDriftFixture(t *testing.T) *helmDriftFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)

	f := &helmDriftFixture{}

	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(f.indexBody))
	})
	// No /charts/ handler: the pre-seeded rows short-circuit via
	// FindByDigest in helm/sync_handler.go's collectFn.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "dp", "phase6 plan 07 helm drift")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "helm", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	trashRoot := filepath.Join(dataRoot, "trash")
	pathStore := storage.NewPathStore(repoRoot)

	auditPath := filepath.Join(dataRoot, "logs", "audit.log")
	_ = os.MkdirAll(filepath.Dir(auditPath), 0o750)
	auditLogger, err := audit.New(db, auditPath, 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	h := helm.NewSyncHandler(helm.SyncDeps{
		DB:         db,
		Path:       pathStore,
		HelmCharts: helmCharts,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		Audit:      auditLogger,
		HTTPClient: srv.Client(),
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
		Trash:      storage.NewTrash(trashRoot),
	})

	f.h = h
	f.db = db
	f.repoID = rid
	f.upstreamURL = srv.URL
	f.trashRoot = trashRoot
	f.dataRoot = dataRoot
	return f
}

func seedHelmChartRow(t *testing.T, f *helmDriftFixture, name, version, hexDigest string) {
	t.Helper()
	ctx := context.Background()
	filename := fmt.Sprintf("%s-%s.tgz", name, version)
	onDisk := filepath.Join(f.dataRoot, "repos", "dp", "helm", "r1", "charts", filename)
	_ = os.MkdirAll(filepath.Dir(onDisk), 0o750)
	if err := os.WriteFile(onDisk, []byte("chart-body-"+filename), 0o640); err != nil {
		t.Fatalf("write %s: %v", onDisk, err)
	}
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, ierr := metadata.NewHelmChartsRepo(f.db).Insert(ctx, tx, &metadata.HelmChart{
			RepoID:   f.repoID,
			Name:     name,
			Version:  version,
			Digest:   "sha256:" + hexDigest,
			Filename: filename,
		})
		return ierr
	}); err != nil {
		t.Fatalf("seed helm chart %s: %v", name, err)
	}
}

// helmIndexEntry renders one index.yaml entry. hexDigest is the raw
// sha256 hex (no "sha256:" prefix) — the helm upstream parser builds
// UpstreamEntry.Digest as "sha256:<hex>".
func helmIndexEntry(name, version, hexDigest string) string {
	return fmt.Sprintf(`  %s:
    - apiVersion: v2
      name: %s
      version: %s
      appVersion: "%s"
      description: %s drift test
      digest: %s
      urls:
        - charts/%s-%s.tgz
`, name, name, version, version, name, hexDigest, name, version)
}

func helmEnableDriftPurge(t *testing.T, db *metadata.DB, repoID int64) {
	t.Helper()
	if _, err := db.Writer.ExecContext(context.Background(),
		`UPDATE repos SET is_mirror = 1, drift_purge = 1 WHERE id = ?`, repoID,
	); err != nil {
		t.Fatalf("enable drift_purge: %v", err)
	}
}

func helmRunDriftSync(t *testing.T, f *helmDriftFixture) int64 {
	t.Helper()
	jobID := seedHelmSyncJobRow(t, f.db, f.repoID)
	payload := map[string]any{"upstream_url": f.upstreamURL}
	pb, _ := json.Marshal(payload)
	if err := f.h.Handle(context.Background(), string(pb), 0, f.repoID, jobID); err != nil {
		t.Fatalf("sync jobID=%d: %v", jobID, err)
	}
	return jobID
}

func helmTrashCount(t *testing.T, trashRoot, kind string) int {
	t.Helper()
	trash := storage.NewTrash(trashRoot)
	entries, err := trash.List(context.Background())
	if err != nil {
		t.Fatalf("trash.List: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func helmQueryDriftAudit(t *testing.T, db *metadata.DB, kind string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err := db.Reader.QueryContext(context.Background(),
			`SELECT details_json FROM audit_log WHERE event_kind = ? ORDER BY id`, kind,
		)
		if err != nil {
			t.Fatalf("query audit: %v", err)
		}
		var out []map[string]any
		for rows.Next() {
			var js string
			_ = rows.Scan(&js)
			var m map[string]any
			_ = json.Unmarshal([]byte(js), &m)
			out = append(out, m)
		}
		_ = rows.Close()
		if len(out) > 0 || time.Now().After(deadline) {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func helmSummaryDriftPurged(t *testing.T, db *metadata.DB, jobID int64) (int64, bool) {
	t.Helper()
	var raw *string
	err := db.Reader.QueryRowContext(context.Background(),
		`SELECT json_extract(summary, '$.drift_purged') FROM sync_jobs WHERE id = ?`, jobID,
	).Scan(&raw)
	if err != nil {
		t.Fatalf("query summary: %v", err)
	}
	if raw == nil {
		return 0, false
	}
	var n int64
	if _, err := fmt.Sscanf(*raw, "%d", &n); err != nil {
		t.Fatalf("parse summary.drift_purged=%q: %v", *raw, err)
	}
	return n, true
}

func TestHelmMirrorSync_DriftPurge_RemovesVanishedUpstreamEntries(t *testing.T) {
	f := newHelmDriftFixture(t)
	helmEnableDriftPurge(t, f.db, f.repoID)

	seedHelmChartRow(t, f, "alpha", "1.0.0", "aaaa")
	seedHelmChartRow(t, f, "beta", "2.0.0", "bbbb")
	seedHelmChartRow(t, f, "gamma", "3.0.0", "cccc")

	// Upstream publishes only alpha + gamma; beta is drift.
	f.setIndex("apiVersion: v1\nentries:\n" +
		helmIndexEntry("alpha", "1.0.0", "aaaa") +
		helmIndexEntry("gamma", "3.0.0", "cccc") +
		"generated: \"2026-04-25T00:00:00Z\"\n")
	jobID := helmRunDriftSync(t, f)

	rows, _ := metadata.NewHelmChartsRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 2 {
		t.Errorf("after sync: rows = %d, want 2 (beta purged)", len(rows))
	}
	for _, r := range rows {
		if r.Name == "beta" {
			t.Errorf("drifted row beta still present")
		}
	}

	if got := helmTrashCount(t, f.trashRoot, "helm_chart_drift"); got != 1 {
		t.Errorf("trash count = %d, want 1", got)
	}

	events := helmQueryDriftAudit(t, f.db, "mirror.drift_purged")
	if len(events) != 1 {
		t.Fatalf("drift_purged audit count = %d, want 1", len(events))
	}
	if p, _ := events[0]["protocol"].(string); p != "helm" {
		t.Errorf("audit.protocol = %v, want helm", events[0]["protocol"])
	}
	if c, _ := events[0]["count"].(float64); int(c) != 1 {
		t.Errorf("audit.count = %v, want 1", events[0]["count"])
	}
	if sample, _ := events[0]["sample"].([]any); len(sample) != 1 || sample[0] != "beta-2.0.0.tgz" {
		t.Errorf("audit.sample = %v, want [beta-2.0.0.tgz]", events[0]["sample"])
	}

	n, present := helmSummaryDriftPurged(t, f.db, jobID)
	if !present || n != 1 {
		t.Errorf("summary.drift_purged = %d present=%v, want 1 true", n, present)
	}
}

// TestHelmMirrorSync_DriftPurge_SkipOnFailedSync proves D-11 for helm
// specifically: a ctx-cancel or upstream error returns before drift,
// the rows stay intact, and no drift audit fires. We force an
// index.yaml 500 so the sync fails at parse time (before drift).
func TestHelmMirrorSync_DriftPurge_SkipOnFailedSync(t *testing.T) {
	f := newHelmDriftFixture(t)
	helmEnableDriftPurge(t, f.db, f.repoID)

	seedHelmChartRow(t, f, "alpha", "1.0.0", "aaaa")
	seedHelmChartRow(t, f, "beta", "2.0.0", "bbbb")
	seedHelmChartRow(t, f, "gamma", "3.0.0", "cccc")

	// Point at an upstream that returns 500 on index.yaml.
	brokenMux := http.NewServeMux()
	brokenMux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusInternalServerError)
	})
	brokenSrv := httptest.NewServer(brokenMux)
	defer brokenSrv.Close()

	jobID := seedHelmSyncJobRow(t, f.db, f.repoID)
	payload := map[string]any{"upstream_url": brokenSrv.URL}
	pb, _ := json.Marshal(payload)
	err := f.h.Handle(context.Background(), string(pb), 0, f.repoID, jobID)
	if err == nil {
		t.Fatalf("expected sync error on 500 upstream")
	}

	rows, _ := metadata.NewHelmChartsRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 3 {
		t.Errorf("after failed sync: rows = %d, want 3 (D-11)", len(rows))
	}
	if got := helmTrashCount(t, f.trashRoot, "helm_chart_drift"); got != 0 {
		t.Errorf("trash count on failed sync = %d, want 0", got)
	}
	events := helmQueryDriftAudit(t, f.db, "mirror.drift_purged")
	if len(events) != 0 {
		t.Errorf("drift_purged audit on failed sync = %d, want 0", len(events))
	}
	if _, present := helmSummaryDriftPurged(t, f.db, jobID); present {
		t.Errorf("summary.drift_purged present on failed sync; want absent")
	}
}

func TestHelmMirrorSync_DriftPurge_EmptyUpstreamGuard(t *testing.T) {
	f := newHelmDriftFixture(t)
	helmEnableDriftPurge(t, f.db, f.repoID)

	seedHelmChartRow(t, f, "alpha", "1.0.0", "aaaa")
	seedHelmChartRow(t, f, "beta", "2.0.0", "bbbb")
	seedHelmChartRow(t, f, "gamma", "3.0.0", "cccc")

	// Upstream index.yaml with no entries.
	f.setIndex("apiVersion: v1\nentries: {}\ngenerated: \"2026-04-25T00:00:00Z\"\n")
	jobID := helmRunDriftSync(t, f)

	rows, _ := metadata.NewHelmChartsRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 3 {
		t.Errorf("after empty-upstream sync: rows = %d, want 3 (D-08 guard)", len(rows))
	}
	if got := helmTrashCount(t, f.trashRoot, "helm_chart_drift"); got != 0 {
		t.Errorf("trash count on guard = %d, want 0", got)
	}
	events := helmQueryDriftAudit(t, f.db, "mirror.drift_purge_skipped")
	if len(events) != 1 {
		t.Fatalf("drift_purge_skipped audit count = %d, want 1", len(events))
	}
	if _, present := helmSummaryDriftPurged(t, f.db, jobID); present {
		t.Errorf("summary.drift_purged present on guard sync; want absent")
	}
}
