package rpm_test

// RPM mirror drift-purge integration test.
//
// Approach: seed 3 rpm_packages rows DIRECTLY via metadata.Insert (the
// alternative — driving the sync handler through fetchAndCommit — would
// require 3 distinct real RPM bodies because Parse reads header bytes
// and UNIQUE(repo_id,name,epoch,version,release,arch) collapses
// duplicates; only one real sample.rpm ships in testdata/). Then run
// ONE sync against an upstream that publishes only 2 of the 3 triples
// — the drift step should detect the 3rd and purge it. This exercises
// the drift pipeline (driftpurge.Run + adapter.Purge + audit + summary)
// end-to-end without relying on a fleet of sample RPMs.
//
// The empty-upstream guard test follows the same shape with a
// zero-entry upstream.

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
	"github.com/vladoportos/omnirepo/internal/storage"
)

type rpmDriftFixture struct {
	h           *rpm.SyncHandler
	db          *metadata.DB
	repoID      int64
	upstreamURL string
	trashRoot   string
	dataRoot    string
	// primaryPkgs is the in-flight primary.xml entry list; tests swap it
	// between syncs.
	primaryPkgs []rpm.PrimaryPkg
}

func (f *rpmDriftFixture) buildPrimaryGZ(t *testing.T) []byte {
	t.Helper()
	root := rpm.PrimaryRoot{
		Xmlns:    "http://linux.duke.edu/metadata/common",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: len(f.primaryPkgs),
		Pkgs:     f.primaryPkgs,
	}
	x, err := xml.Marshal(&root)
	if err != nil {
		t.Fatalf("marshal primary: %v", err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(x)
	_ = w.Close()
	return gz.Bytes()
}

func newRPMDriftFixture(t *testing.T) *rpmDriftFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	rpmPkgs := metadata.NewRPMPackagesRepo(db)
	scans := metadata.NewScansRepo(db)

	f := &rpmDriftFixture{}

	mux := http.NewServeMux()
	mux.HandleFunc("/repodata/repomd.xml", func(w http.ResponseWriter, r *http.Request) {
		gz := f.buildPrimaryGZ(t)
		repomd := rpm.RepomdRoot{
			Xmlns:    "http://linux.duke.edu/metadata/repo",
			XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
			Revision: 1,
			Data: []rpm.RepomdData{{
				Type:     "primary",
				Checksum: rpm.RepomdCksum{Type: "sha256", Value: "abc"},
				Location: rpm.RepomdLoc{Href: "repodata/primary.xml.gz"},
				Size:     int64(len(gz)),
			}},
		}
		x, _ := xml.Marshal(&repomd)
		_, _ = w.Write(x)
	})
	mux.HandleFunc("/repodata/primary.xml.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(f.buildPrimaryGZ(t))
	})
	// No /Packages/ handler: the seeded rows are already present in the
	// DB via FindByDigest idempotency gate, so the fetch loop skips them
	// entirely. Drift runs on the already-seeded set against the
	// upstream key set parsed from primary.xml.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "dp", "rpm drift")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "rpm", "r1", "", nil, nil, nil)
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

	h := rpm.NewSyncHandler(rpm.SyncDeps{
		DB:          db,
		Path:        pathStore,
		RPMPackages: rpmPkgs,
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
	f.upstreamURL = srv.URL
	f.trashRoot = trashRoot
	f.dataRoot = dataRoot
	return f
}

// seedRPMRow inserts a rpm_packages row directly. Matching the
// primary.xml entry with the same (name, version, arch) + digest makes
// the fetch loop skip via FindByDigest. The on-disk .rpm file is
// written separately so Trash.MoveWithSnapshot has something to move.
func seedRPMRow(t *testing.T, f *rpmDriftFixture, projectName, name, version, arch, digest string) {
	t.Helper()
	ctx := context.Background()
	filename := fmt.Sprintf("%s-%s-1.%s.rpm", name, version, arch)
	// Write a placeholder on-disk body. Trash.Move tolerates missing
	// sources too, but writing one exercises the full move path.
	onDisk := filepath.Join(f.dataRoot, "repos", projectName, "rpm", "r1", "Packages", filename)
	_ = os.MkdirAll(filepath.Dir(onDisk), 0o750)
	if err := os.WriteFile(onDisk, []byte("rpm-body-"+filename), 0o640); err != nil {
		t.Fatalf("write %s: %v", onDisk, err)
	}
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, ierr := metadata.NewRPMPackagesRepo(f.db).Insert(ctx, tx, &metadata.RPMPackage{
			RepoID:   f.repoID,
			Name:     name,
			Epoch:    0,
			Version:  version,
			Release:  "1",
			Arch:     arch,
			Digest:   digest,
			Filename: filename,
		})
		return ierr
	}); err != nil {
		t.Fatalf("seed rpm row %s: %v", name, err)
	}
}

// rpmPrimaryEntry builds a PrimaryPkg matching seedRPMRow's projection.
// hexDigest is the raw sha256 hex (no "sha256:" prefix) — the upstream
// parser re-prefixes before yielding the UpstreamEntry.Digest. Passing
// a hex that matches seedRPMRow's "sha256:<hex>" digest short-circuits
// the fetch loop via FindByDigest (we serve no /Packages/ bytes).
func rpmPrimaryEntry(name, version, arch, hexDigest string) rpm.PrimaryPkg {
	return rpm.PrimaryPkg{
		Type:     "rpm",
		Name:     name,
		Arch:     arch,
		Version:  rpm.PrimaryVer{Epoch: "0", Ver: version, Rel: "1"},
		Checksum: rpm.PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: hexDigest},
		Summary:  name + " test rpm",
		Time:     rpm.PrimaryTime{File: 1700000000, Build: 1700000000},
		Size:     rpm.PrimarySize{Package: 1024},
		Location: rpm.PrimaryLoc{Href: fmt.Sprintf("Packages/%s-%s-1.%s.rpm", name, version, arch)},
	}
}

func rpmEnableDriftPurge(t *testing.T, db *metadata.DB, repoID int64) {
	t.Helper()
	if _, err := db.Writer.ExecContext(context.Background(),
		`UPDATE repos SET is_mirror = 1, drift_purge = 1 WHERE id = ?`, repoID,
	); err != nil {
		t.Fatalf("enable drift_purge: %v", err)
	}
}

func rpmRunSync(t *testing.T, f *rpmDriftFixture) int64 {
	t.Helper()
	jobID := seedRPMSyncJob(t, f.db, f.repoID)
	payload := map[string]any{"upstream_url": f.upstreamURL}
	pb, _ := json.Marshal(payload)
	if err := f.h.Handle(context.Background(), string(pb), 0, f.repoID, jobID); err != nil {
		t.Fatalf("sync jobID=%d: %v", jobID, err)
	}
	return jobID
}

func rpmTrashCount(t *testing.T, trashRoot, kind string) int {
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

func rpmQueryDriftAudit(t *testing.T, db *metadata.DB, kind string) []map[string]any {
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

func rpmSummaryDriftPurged(t *testing.T, db *metadata.DB, jobID int64) (int64, bool) {
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

func TestRPMMirrorSync_DriftPurge_RemovesVanishedUpstreamEntries(t *testing.T) {
	f := newRPMDriftFixture(t)
	rpmEnableDriftPurge(t, f.db, f.repoID)

	// Seed 3 rows directly. Row digests use the "sha256:<hex>" form
	// ingest writes; primary.xml Checksum.Value carries raw hex and
	// the parser re-prefixes — so FindByDigest sees the same string.
	seedRPMRow(t, f, "dp", "alpha", "1.0", "x86_64", "sha256:aaaa")
	seedRPMRow(t, f, "dp", "beta", "2.0", "x86_64", "sha256:bbbb")
	seedRPMRow(t, f, "dp", "gamma", "3.0", "x86_64", "sha256:cccc")

	// Upstream publishes only alpha + gamma; beta is drift.
	f.primaryPkgs = []rpm.PrimaryPkg{
		rpmPrimaryEntry("alpha", "1.0", "x86_64", "aaaa"),
		rpmPrimaryEntry("gamma", "3.0", "x86_64", "cccc"),
	}
	jobID := rpmRunSync(t, f)

	rows, _ := metadata.NewRPMPackagesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 2 {
		t.Errorf("after sync: rows = %d, want 2 (beta purged)", len(rows))
	}
	for _, r := range rows {
		if r.Name == "beta" {
			t.Errorf("drifted row beta still present")
		}
	}

	if got := rpmTrashCount(t, f.trashRoot, "rpm_package_drift"); got != 1 {
		t.Errorf("trash count = %d, want 1", got)
	}

	events := rpmQueryDriftAudit(t, f.db, "mirror.drift_purged")
	if len(events) != 1 {
		t.Fatalf("drift_purged audit count = %d, want 1", len(events))
	}
	if p, _ := events[0]["protocol"].(string); p != "rpm" {
		t.Errorf("audit.protocol = %v, want rpm", events[0]["protocol"])
	}
	if c, _ := events[0]["count"].(float64); int(c) != 1 {
		t.Errorf("audit.count = %v, want 1", events[0]["count"])
	}

	n, present := rpmSummaryDriftPurged(t, f.db, jobID)
	if !present || n != 1 {
		t.Errorf("summary.drift_purged = %d present=%v, want 1 true", n, present)
	}
}

func TestRPMMirrorSync_DriftPurge_EmptyUpstreamGuard(t *testing.T) {
	f := newRPMDriftFixture(t)
	rpmEnableDriftPurge(t, f.db, f.repoID)

	seedRPMRow(t, f, "dp", "alpha", "1.0", "x86_64", "sha256:aaaa")
	seedRPMRow(t, f, "dp", "beta", "2.0", "x86_64", "sha256:bbbb")
	seedRPMRow(t, f, "dp", "gamma", "3.0", "x86_64", "sha256:cccc")

	// Upstream returns zero entries; guard must trip.
	f.primaryPkgs = nil
	jobID := rpmRunSync(t, f)

	rows, _ := metadata.NewRPMPackagesRepo(f.db).ListByRepo(context.Background(), f.repoID)
	if len(rows) != 3 {
		t.Errorf("after empty-upstream sync: rows = %d, want 3 (guard tripped)", len(rows))
	}
	if got := rpmTrashCount(t, f.trashRoot, "rpm_package_drift"); got != 0 {
		t.Errorf("trash count on guard = %d, want 0", got)
	}
	events := rpmQueryDriftAudit(t, f.db, "mirror.drift_purge_skipped")
	if len(events) != 1 {
		t.Fatalf("drift_purge_skipped audit count = %d, want 1", len(events))
	}
	if _, present := rpmSummaryDriftPurged(t, f.db, jobID); present {
		t.Errorf("summary.drift_purged present on guard sync; want absent")
	}
}
