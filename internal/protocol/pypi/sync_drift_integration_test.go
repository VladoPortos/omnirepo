package pypi_test

// v1.5 Phase 6 Plan 06-07 (DRIFTPURGE-01..05) — PyPI mirror drift-purge
// integration test. Seed 3 wheels via a fake /simple/ upstream, enable
// repo.drift_purge, then drop one entry from the upstream response and
// re-sync — assert the missing wheel lands in trash with kind
// pypi_file_drift + the mirror.drift_purged audit event emits + the
// sync_jobs.summary.drift_purged int == 1.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// pypiDriftFixture is the drift-test analogue of newPyPIProgressFixture.
// Upstream file set is mutable via setUpstream so a second sync can drop
// entries between runs (the drift trigger).
type pypiDriftFixture struct {
	h           *pypi.SyncHandler
	db          *metadata.DB
	repoID      int64
	upstreamURL string
	trashRoot   string
	dataRoot    string
	repoName    string
	projName    string
	// upstreamFiles holds the per-filename (bytes, sha256) currently being
	// served. setUpstream swaps this atomically between syncs.
	upstreamFiles atomic.Value // map[string]pypiFileBytes
}

type pypiFileBytes struct {
	bytes  []byte
	digest string
}

func (f *pypiDriftFixture) setUpstream(t *testing.T, filenames ...string) {
	t.Helper()
	files := make(map[string]pypiFileBytes, len(filenames))
	for _, fn := range filenames {
		b := []byte("wheel-body-" + fn + strings.Repeat("x", 50))
		sum := sha256.Sum256(b)
		files[fn] = pypiFileBytes{bytes: b, digest: hex.EncodeToString(sum[:])}
	}
	f.upstreamFiles.Store(files)
}

func newPyPIDriftFixture(t *testing.T) *pypiDriftFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	pypiFiles := metadata.NewPyPIFilesRepo(db)
	scans := metadata.NewScansRepo(db)

	f := &pypiDriftFixture{}
	f.upstreamFiles.Store(map[string]pypiFileBytes{})

	mux := http.NewServeMux()
	// /simple/ — one project "acme".
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/simple/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(`{"meta":{"api-version":"1.0"},"projects":[{"name":"acme"}]}`))
	})
	// /simple/acme/ — dynamic file list from upstreamFiles.
	mux.HandleFunc("/simple/acme/", func(w http.ResponseWriter, r *http.Request) {
		files := f.upstreamFiles.Load().(map[string]pypiFileBytes)
		var parts []string
		for fn, fb := range files {
			parts = append(parts, fmt.Sprintf(
				`{"filename":%q,"url":"/packages/%s","hashes":{"sha256":%q},"size":%d}`,
				fn, fn, fb.digest, len(fb.bytes),
			))
		}
		body := fmt.Sprintf(`{"meta":{"api-version":"1.0"},"name":"acme","files":[%s]}`, strings.Join(parts, ","))
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(body))
	})
	// /packages/<fn> — serve bytes from upstreamFiles.
	mux.HandleFunc("/packages/", func(w http.ResponseWriter, r *http.Request) {
		fn := strings.TrimPrefix(r.URL.Path, "/packages/")
		files := f.upstreamFiles.Load().(map[string]pypiFileBytes)
		fb, ok := files[fn]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fb.bytes)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	const projName = "dp"
	const repoName = "r1"
	pid, err := projectsRepo.Create(ctx, projName, "phase6 plan 07 pypi drift")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "pypi", repoName, "", nil, nil, nil)
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

	h := pypi.NewSyncHandler(pypi.SyncDeps{
		DB:         db,
		Path:       pathStore,
		PyPIFiles:  pypiFiles,
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
	f.repoName = repoName
	f.projName = projName
	return f
}

func pypiEnableDriftPurge(t *testing.T, db *metadata.DB, repoID int64) {
	t.Helper()
	if _, err := db.Writer.ExecContext(context.Background(),
		`UPDATE repos SET is_mirror = 1, drift_purge = 1 WHERE id = ?`, repoID,
	); err != nil {
		t.Fatalf("enable drift_purge: %v", err)
	}
}

func pypiRunSync(t *testing.T, f *pypiDriftFixture) int64 {
	t.Helper()
	jobID := seedPyPISyncJob(t, f.db, f.repoID)
	payload := map[string]any{"upstream_url": f.upstreamURL}
	pb, _ := json.Marshal(payload)
	if err := f.h.Handle(context.Background(), string(pb), 0, f.repoID, jobID); err != nil {
		t.Fatalf("sync jobID=%d: %v", jobID, err)
	}
	return jobID
}

func pypiTrashCount(t *testing.T, trashRoot, kind string) int {
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

func pypiQueryDriftAudit(t *testing.T, db *metadata.DB, kind string) []map[string]any {
	t.Helper()
	// audit.Record flushes best-effort; allow a short poll so CI doesn't flake.
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

func pypiSummaryDriftPurged(t *testing.T, db *metadata.DB, jobID int64) (int64, bool) {
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

// TestPyPIMirrorSync_DriftPurge_RemovesVanishedUpstreamEntries is the
// headline test. Seed 3 wheels, sync, drop 1 wheel from upstream, sync
// again — assert the missing wheel lands in trash, the audit event
// fires with count=1 and the sorted-lex sample, and the sync_jobs
// summary carries drift_purged=1.
func TestPyPIMirrorSync_DriftPurge_RemovesVanishedUpstreamEntries(t *testing.T) {
	f := newPyPIDriftFixture(t)
	pypiEnableDriftPurge(t, f.db, f.repoID)

	// Sync #1: seed 3 wheels.
	f.setUpstream(t, "foo-1.0.0.tar.gz", "foo-1.0.1.tar.gz", "bar-2.0.0.tar.gz")
	pypiRunSync(t, f)

	rows, err := metadata.NewPyPIFilesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("after sync1: rows=%d err=%v, want 3", len(rows), err)
	}
	if got := pypiTrashCount(t, f.trashRoot, "pypi_file_drift"); got != 0 {
		t.Errorf("after sync1: trash count = %d, want 0", got)
	}

	// Sync #2: drop foo-1.0.1.tar.gz.
	f.setUpstream(t, "foo-1.0.0.tar.gz", "bar-2.0.0.tar.gz")
	jobID2 := pypiRunSync(t, f)

	rows2, _ := metadata.NewPyPIFilesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows2) != 2 {
		t.Errorf("after sync2: rows = %d, want 2", len(rows2))
	}
	for _, r := range rows2 {
		if r.Filename == "foo-1.0.1.tar.gz" {
			t.Errorf("drifted row foo-1.0.1.tar.gz still present")
		}
	}

	if got := pypiTrashCount(t, f.trashRoot, "pypi_file_drift"); got != 1 {
		t.Errorf("after sync2: trash count = %d, want 1", got)
	}

	// Audit row.
	events := pypiQueryDriftAudit(t, f.db, "mirror.drift_purged")
	if len(events) != 1 {
		t.Fatalf("drift_purged audit count = %d, want 1", len(events))
	}
	ev := events[0]
	if p, _ := ev["protocol"].(string); p != "pypi" {
		t.Errorf("audit.protocol = %v, want pypi", ev["protocol"])
	}
	if c, _ := ev["count"].(float64); int(c) != 1 {
		t.Errorf("audit.count = %v, want 1", ev["count"])
	}
	if sample, _ := ev["sample"].([]any); len(sample) != 1 || sample[0] != "foo-1.0.1.tar.gz" {
		t.Errorf("audit.sample = %v, want [foo-1.0.1.tar.gz]", ev["sample"])
	}

	// sync_jobs.summary.drift_purged.
	n, present := pypiSummaryDriftPurged(t, f.db, jobID2)
	if !present {
		t.Fatal("sync_jobs.summary.drift_purged absent; want 1")
	}
	if n != 1 {
		t.Errorf("summary.drift_purged = %d, want 1", n)
	}
}

// TestPyPIMirrorSync_DriftPurge_SkipOnFailedSync proves D-11: a sync
// that fails mid-flight must never reach the drift step. We force
// upstream 500 on the project page; sync returns non-nil; no drift
// audit fires; sync_jobs.summary stays without drift_purged key.
func TestPyPIMirrorSync_DriftPurge_SkipOnFailedSync(t *testing.T) {
	f := newPyPIDriftFixture(t)
	pypiEnableDriftPurge(t, f.db, f.repoID)

	// Sync #1: seed 3 wheels.
	f.setUpstream(t, "foo-1.0.0.tar.gz", "foo-1.0.1.tar.gz", "bar-2.0.0.tar.gz")
	pypiRunSync(t, f)

	// Swap the upstream handler so /simple/acme/ returns 500 on sync #2.
	// We do this by creating a fresh fixture that shares the same DB but
	// points at a broken mux. Simpler: set upstream to empty + wrap srv
	// handler — but cleaner is to direct-point the handler at a broken
	// URL via a one-shot poisoned map. We pick the one-shot swap by
	// invalidating upstream entries for this sync via a separate server.
	brokenMux := http.NewServeMux()
	brokenMux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusInternalServerError)
	})
	brokenSrv := httptest.NewServer(brokenMux)
	defer brokenSrv.Close()

	jobID2 := seedPyPISyncJob(t, f.db, f.repoID)
	payload := map[string]any{"upstream_url": brokenSrv.URL}
	pb, _ := json.Marshal(payload)
	err := f.h.Handle(context.Background(), string(pb), 0, f.repoID, jobID2)
	if err == nil {
		t.Fatalf("sync #2: expected error from broken upstream, got nil")
	}

	// Rows intact.
	rows, _ := metadata.NewPyPIFilesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 3 {
		t.Errorf("after failed sync: rows = %d, want 3 (drift must not run)", len(rows))
	}
	// Zero drift trash holders.
	if got := pypiTrashCount(t, f.trashRoot, "pypi_file_drift"); got != 0 {
		t.Errorf("after failed sync: trash count = %d, want 0 (D-11)", got)
	}
	// Zero drift_purged audit events.
	events := pypiQueryDriftAudit(t, f.db, "mirror.drift_purged")
	if len(events) != 0 {
		t.Errorf("drift_purged audit on failed sync = %d, want 0", len(events))
	}
	// sync_jobs.summary.drift_purged absent for jobID2.
	if _, present := pypiSummaryDriftPurged(t, f.db, jobID2); present {
		t.Errorf("summary.drift_purged present on failed sync; want absent (D-21)")
	}
}

// TestPyPIMirrorSync_DriftPurge_EmptyUpstreamGuard proves D-08: when
// the upstream returns zero entries but >0 local rows exist, drift is
// SKIPPED and mirror.drift_purge_skipped fires.
func TestPyPIMirrorSync_DriftPurge_EmptyUpstreamGuard(t *testing.T) {
	f := newPyPIDriftFixture(t)
	pypiEnableDriftPurge(t, f.db, f.repoID)

	f.setUpstream(t, "foo-1.0.0.tar.gz", "foo-1.0.1.tar.gz", "bar-2.0.0.tar.gz")
	pypiRunSync(t, f)

	// Sync #2: upstream goes empty (project page returns no files).
	f.setUpstream(t /* no filenames */)
	jobID2 := pypiRunSync(t, f)

	// Rows intact (guard tripped).
	rows, _ := metadata.NewPyPIFilesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 3 {
		t.Errorf("after empty-upstream sync: rows = %d, want 3 (D-08 guard)", len(rows))
	}
	// Zero trash holders.
	if got := pypiTrashCount(t, f.trashRoot, "pypi_file_drift"); got != 0 {
		t.Errorf("after empty-upstream sync: trash count = %d, want 0", got)
	}
	// Skipped audit event fired.
	events := pypiQueryDriftAudit(t, f.db, "mirror.drift_purge_skipped")
	if len(events) != 1 {
		t.Fatalf("drift_purge_skipped audit count = %d, want 1", len(events))
	}
	ev := events[0]
	if reason, _ := ev["reason"].(string); reason != "upstream_empty" {
		t.Errorf("skipped.reason = %v, want upstream_empty", ev["reason"])
	}
	if lc, _ := ev["local_count"].(float64); int(lc) != 3 {
		t.Errorf("skipped.local_count = %v, want 3", ev["local_count"])
	}
	// sync_jobs.summary.drift_purged absent (D-21 absence on skipped).
	if _, present := pypiSummaryDriftPurged(t, f.db, jobID2); present {
		t.Errorf("summary.drift_purged present on empty-upstream; want absent (D-21)")
	}
}
