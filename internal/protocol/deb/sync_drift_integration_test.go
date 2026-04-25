package deb_test

// v1.5 Phase 6 Plan 06-07 (DRIFTPURGE-01..05) — DEB mirror drift-purge
// integration test.
//
// Approach matches the RPM test: seed 3 deb_packages rows directly
// (building real .deb ar archives per-package would add 100+ LoC that
// doesn't exercise drift), then run one sync whose upstream Packages
// publishes only 2 of the 3 triples. The drift step detects the 3rd
// and purges it.

import (
	"bytes"
	"compress/gzip"
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
	"github.com/dxc-internal/omnirepo/internal/protocol/deb"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

type debDriftFixture struct {
	h           *deb.SyncHandler
	db          *metadata.DB
	repoID      int64
	suiteID     int64
	upstreamURL string
	trashRoot   string
	dataRoot    string
	// packagesBody is the plaintext apt Packages body served (gzipped) at
	// /dists/stable/main/binary-amd64/Packages.gz. Tests rewrite this
	// between syncs.
	packagesBody string
}

func (f *debDriftFixture) setPackages(body string) {
	f.packagesBody = body
}

func newDEBDriftFixture(t *testing.T) *debDriftFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	debPkgs := metadata.NewDEBPackagesRepo(db)
	aptSuites := metadata.NewAptSuitesRepo(db)
	scans := metadata.NewScansRepo(db)

	f := &debDriftFixture{}

	releaseBody := "Suite: stable\nCodename: stable\nComponents: main\nArchitectures: amd64\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/dists/stable/InRelease", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // force Release fallback
	})
	mux.HandleFunc("/dists/stable/Release", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(releaseBody))
	})
	mux.HandleFunc("/dists/stable/main/binary-amd64/Packages.gz", func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(f.packagesBody))
		_ = gz.Close()
		_, _ = w.Write(buf.Bytes())
	})
	// No /pool/ handler: pre-seeded rows short-circuit via FindByDigest.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "dp", "phase6 plan 07 deb drift")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "deb", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Seed the suite row so seedDEBRow can reference it.
	var suiteID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, ierr := aptSuites.Insert(ctx, tx, rid, "stable", "main", "amd64")
		suiteID = v
		return ierr
	}); err != nil {
		t.Fatalf("seed apt_suite: %v", err)
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

	h := deb.NewSyncHandler(deb.SyncDeps{
		DB:          db,
		Path:        pathStore,
		DEBPackages: debPkgs,
		AptSuites:   aptSuites,
		Repos:       reposRepo,
		Projects:    projectsRepo,
		Scans:       scans,
		Audit:       auditLogger,
		HTTPClient:  srv.Client(),
		RepoRoot:    repoRoot,
		Cfg:         config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:    metadata.NewSyncJobsRepo(db),
		Trash:       storage.NewTrash(trashRoot),
	})

	f.h = h
	f.db = db
	f.repoID = rid
	f.suiteID = suiteID
	f.upstreamURL = srv.URL
	f.trashRoot = trashRoot
	f.dataRoot = dataRoot
	return f
}

func seedDEBRow(t *testing.T, f *debDriftFixture, pkg, version, arch, hexDigest string) {
	t.Helper()
	ctx := context.Background()
	filename := fmt.Sprintf("%s_%s_%s.deb", pkg, version, arch)
	poolPath := fmt.Sprintf("pool/main/%s/%s/%s", pkg[:1], pkg, filename)
	// Place the on-disk artifact so Trash.MoveWithSnapshot has a real
	// source file to rename. Path mirrors the adapter's pathFn in
	// deb/sync_handler.go's drift block (projectName/deb/repoName/rest).
	onDisk := filepath.Join(f.dataRoot, "repos", "dp", "deb", "r1", poolPath)
	_ = os.MkdirAll(filepath.Dir(onDisk), 0o750)
	if err := os.WriteFile(onDisk, []byte("deb-body-"+filename), 0o640); err != nil {
		t.Fatalf("write %s: %v", onDisk, err)
	}
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, ierr := metadata.NewDEBPackagesRepo(f.db).Insert(ctx, tx, &metadata.DEBPackage{
			RepoID:          f.repoID,
			SuiteID:         f.suiteID,
			Package:         pkg,
			Version:         version,
			Architecture:    arch,
			Digest:          "sha256:" + hexDigest,
			Filename:        filename,
			StoragePoolPath: poolPath,
		})
		return ierr
	}); err != nil {
		t.Fatalf("seed deb row %s: %v", pkg, err)
	}
}

// debPackagesEntry renders a single stanza compatible with apt Packages
// format. hexDigest matches seedDEBRow's "sha256:<hex>" seed digest so
// the parser's digest prefix lets FindByDigest short-circuit the fetch.
func debPackagesEntry(pkg, version, arch, hexDigest string) string {
	filename := fmt.Sprintf("pool/main/%s/%s/%s_%s_%s.deb", pkg[:1], pkg, pkg, version, arch)
	return fmt.Sprintf(`Package: %s
Version: %s
Architecture: %s
Filename: %s
Size: 1024
SHA256: %s
Description: %s drift test

`, pkg, version, arch, filename, hexDigest, pkg)
}

func debEnableDriftPurge(t *testing.T, db *metadata.DB, repoID int64) {
	t.Helper()
	if _, err := db.Writer.ExecContext(context.Background(),
		`UPDATE repos SET is_mirror = 1, drift_purge = 1 WHERE id = ?`, repoID,
	); err != nil {
		t.Fatalf("enable drift_purge: %v", err)
	}
}

func debRunSync(t *testing.T, f *debDriftFixture) int64 {
	t.Helper()
	jobID := seedDEBSyncJob(t, f.db, f.repoID)
	// Filter Suites=[stable] so the handler targets the one suite we serve.
	payload := map[string]any{
		"upstream_url": f.upstreamURL,
		"filter":       map[string]any{"suites": []string{"stable"}},
	}
	pb, _ := json.Marshal(payload)
	if err := f.h.Handle(context.Background(), string(pb), 0, f.repoID, jobID); err != nil {
		t.Fatalf("sync jobID=%d: %v", jobID, err)
	}
	return jobID
}

func debTrashCount(t *testing.T, trashRoot, kind string) int {
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

func debQueryDriftAudit(t *testing.T, db *metadata.DB, kind string) []map[string]any {
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

func debSummaryDriftPurged(t *testing.T, db *metadata.DB, jobID int64) (int64, bool) {
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

func TestDEBMirrorSync_DriftPurge_RemovesVanishedUpstreamEntries(t *testing.T) {
	f := newDEBDriftFixture(t)
	debEnableDriftPurge(t, f.db, f.repoID)

	// Seed 3 rows.
	seedDEBRow(t, f, "alpha", "1.0", "amd64", "aaaa")
	seedDEBRow(t, f, "beta", "2.0", "amd64", "bbbb")
	seedDEBRow(t, f, "gamma", "3.0", "amd64", "cccc")

	// Upstream publishes only alpha + gamma; beta is drift.
	f.setPackages(
		debPackagesEntry("alpha", "1.0", "amd64", "aaaa") +
			debPackagesEntry("gamma", "3.0", "amd64", "cccc"),
	)
	jobID := debRunSync(t, f)

	rows, _ := metadata.NewDEBPackagesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 2 {
		t.Errorf("after sync: rows = %d, want 2 (beta purged)", len(rows))
	}
	for _, r := range rows {
		if r.Package == "beta" {
			t.Errorf("drifted row beta still present")
		}
	}

	if got := debTrashCount(t, f.trashRoot, "deb_package_drift"); got != 1 {
		t.Errorf("trash count = %d, want 1", got)
	}

	events := debQueryDriftAudit(t, f.db, "mirror.drift_purged")
	if len(events) != 1 {
		t.Fatalf("drift_purged audit count = %d, want 1", len(events))
	}
	if p, _ := events[0]["protocol"].(string); p != "deb" {
		t.Errorf("audit.protocol = %v, want deb", events[0]["protocol"])
	}
	if c, _ := events[0]["count"].(float64); int(c) != 1 {
		t.Errorf("audit.count = %v, want 1", events[0]["count"])
	}

	n, present := debSummaryDriftPurged(t, f.db, jobID)
	if !present || n != 1 {
		t.Errorf("summary.drift_purged = %d present=%v, want 1 true", n, present)
	}
}

func TestDEBMirrorSync_DriftPurge_EmptyUpstreamGuard(t *testing.T) {
	f := newDEBDriftFixture(t)
	debEnableDriftPurge(t, f.db, f.repoID)

	seedDEBRow(t, f, "alpha", "1.0", "amd64", "aaaa")
	seedDEBRow(t, f, "beta", "2.0", "amd64", "bbbb")
	seedDEBRow(t, f, "gamma", "3.0", "amd64", "cccc")

	f.setPackages("") // empty Packages.gz
	jobID := debRunSync(t, f)

	rows, _ := metadata.NewDEBPackagesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 3 {
		t.Errorf("after empty-upstream sync: rows = %d, want 3 (D-08 guard)", len(rows))
	}
	if got := debTrashCount(t, f.trashRoot, "deb_package_drift"); got != 0 {
		t.Errorf("trash count on guard = %d, want 0", got)
	}
	events := debQueryDriftAudit(t, f.db, "mirror.drift_purge_skipped")
	if len(events) != 1 {
		t.Fatalf("drift_purge_skipped audit count = %d, want 1", len(events))
	}
	if _, present := debSummaryDriftPurged(t, f.db, jobID); present {
		t.Errorf("summary.drift_purged present on guard sync; want absent")
	}
}
