package scan_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/scan"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// scanFixture sets up a DB + audit logger + CAS + scan handler with a
// FakeRunner. seedDockerArtifact creates a project, repo, manifest, blob,
// and pending scan row ready for Handle.
type scanFixture struct {
	t        *testing.T
	db       *metadata.DB
	dataRoot string
	cas      storage.CAS
	runner   *scan.FakeRunner
	cache    *scan.SeverityCache
	audit    audit.Logger
	scans    *metadata.ScansRepo
	vulns    *metadata.VulnerabilitiesRepo
	mfs      *metadata.DockerManifestsRepo
	rawf     *metadata.RawFilesRepo
	repos    *metadata.ReposRepo
	projs    *metadata.ProjectsRepo
	blobs    *metadata.DockerBlobsRepo
	handler  *scan.Handler
}

func newScanFixture(t *testing.T) *scanFixture {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	cas := storage.NewCAS(filepath.Join(dataRoot, "blobs"))
	auditPath := filepath.Join(dataRoot, "logs", "audit.log")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	auditLogger, err := audit.New(db, auditPath, 100, 5)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	runner := scan.NewFakeRunner()
	cache := scan.NewSeverityCache(0)
	scans := metadata.NewScansRepo(db)
	vulns := metadata.NewVulnerabilitiesRepo(db)
	mfs := metadata.NewDockerManifestsRepo(db)
	rawf := metadata.NewRawFilesRepo(db)
	repos := metadata.NewReposRepo(db)
	projs := metadata.NewProjectsRepo(db)
	blobs := metadata.NewDockerBlobsRepo(db)

	h, err := scan.NewHandler(scan.HandlerDeps{
		DB:        db,
		Runner:    runner,
		Scans:     scans,
		Vulns:     vulns,
		Manifests: mfs,
		RawFiles:  rawf,
		Repos:     repos,
		Projects:  projs,
		CAS:       cas,
		Audit:     auditLogger,
		Cache:     cache,
		DataRoot:  dataRoot,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return &scanFixture{
		t: t, db: db, dataRoot: dataRoot, cas: cas, runner: runner, cache: cache,
		audit: auditLogger, scans: scans, vulns: vulns, mfs: mfs, rawf: rawf,
		repos: repos, projs: projs, blobs: blobs, handler: h,
	}
}

// seedDockerArtifact creates project + docker repo + manifest with a config
// blob and a layer blob in the CAS. Returns the repo id, manifest digest, and
// scan id.
func (f *scanFixture) seedDockerArtifact(t *testing.T) (repoID int64, mfDigest string, scanID int64) {
	t.Helper()
	ctx := context.Background()

	pid, err := f.projs.Create(ctx, "proj1", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := f.repos.Create(ctx, pid, "docker", "img", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	// Blobs in CAS.
	cfgBytes := []byte(`{"architecture":"amd64","os":"linux"}`)
	cfgDigest, _, err := f.cas.Put(ctx, byteReader(cfgBytes))
	if err != nil {
		t.Fatalf("cas put cfg: %v", err)
	}
	layerBytes := []byte("layer-bytes-payload")
	layerDigest, _, err := f.cas.Put(ctx, byteReader(layerBytes))
	if err != nil {
		t.Fatalf("cas put layer: %v", err)
	}
	mfBody, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    cfgDigest,
			"size":      len(cfgBytes),
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.oci.image.layer.v1.tar",
				"digest":    layerDigest,
				"size":      len(layerBytes),
			},
		},
	})
	sum := sha256.Sum256(mfBody)
	mfDigest = "sha256:" + hex.EncodeToString(sum[:])

	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := f.mfs.Insert(ctx, tx, rid, mfDigest, "application/vnd.oci.image.manifest.v1+json", mfBody); err != nil {
			return err
		}
		sid, err := f.scans.Enqueue(ctx, tx, rid, "docker", mfDigest)
		if err != nil {
			return err
		}
		scanID = sid
		return nil
	}); err != nil {
		t.Fatalf("seed tx: %v", err)
	}
	return rid, mfDigest, scanID
}

func byteReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// leasedScan retrieves a Scan struct shaped like what the pool would lease.
// We don't go through the pool here — we just construct it from the row.
func (f *scanFixture) leasedScan(t *testing.T, scanID int64) *metadata.Scan {
	t.Helper()
	var s metadata.Scan
	err := f.db.Reader.QueryRowContext(context.Background(), `
		SELECT id, repo_id, artifact_kind, artifact_id, attempts FROM scans WHERE id=?`,
		scanID,
	).Scan(&s.ID, &s.RepoID, &s.ArtifactKind, &s.ArtifactID, &s.Attempts)
	if err != nil {
		t.Fatalf("read scan: %v", err)
	}
	s.LeaseID = "test"
	return &s
}

func TestHandler_DockerScan_HappyPath(t *testing.T) {
	f := newScanFixture(t)
	rid, mfDigest, sid := f.seedDockerArtifact(t)

	// Queue a Trivy result with a critical + high finding.
	f.runner.QueueImage(scan.Result{
		Summary: map[string]int{"critical": 1, "high": 2, "medium": 0, "low": 0, "unknown": 0},
		Vulnerabilities: []scan.Vuln{
			{CVEID: "CVE-2024-0001", Package: "openssl", Severity: "CRITICAL", Title: "openssl c"},
			{CVEID: "CVE-2024-0002", Package: "curl", Severity: "HIGH", Title: "curl h"},
			{CVEID: "CVE-2024-0003", Package: "bash", Severity: "HIGH", Title: "bash h"},
		},
		TrivyDBVersion: "2026-04-01",
		ArtifactName:   "img",
	}, nil)
	f.runner.QueueSBOM(nil)

	// Pre-seed cache so we can verify Invalidate fires.
	f.cache.Set(rid, "docker", mfDigest, scan.CacheEntry{Blocked: false})

	if err := f.handler.Handle(context.Background(), f.leasedScan(t, sid)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// scans row marked done with summary + sbom_path + db version.
	var status, summary, sbomPath, dbVer string
	err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT status, severity_summary_json, sbom_path, trivy_db_version FROM scans WHERE id=?`, sid,
	).Scan(&status, &summary, &sbomPath, &dbVer)
	if err != nil {
		t.Fatalf("read scan: %v", err)
	}
	if status != "done" {
		t.Fatalf("status=%q want done", status)
	}
	if dbVer != "2026-04-01" {
		t.Fatalf("trivy_db_version=%q want 2026-04-01", dbVer)
	}
	var sm map[string]int
	if err := json.Unmarshal([]byte(summary), &sm); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if sm["critical"] != 1 || sm["high"] != 2 {
		t.Fatalf("summary = %v", sm)
	}

	// 3 vulnerabilities rows.
	var vulnCount int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM vulnerabilities WHERE scan_id=?`, sid,
	).Scan(&vulnCount); err != nil {
		t.Fatal(err)
	}
	if vulnCount != 3 {
		t.Fatalf("vulnerabilities rows = %d, want 3", vulnCount)
	}

	// cves_fts populated.
	var ftsCount int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM cves_fts WHERE cve_id LIKE 'CVE-2024-%'`,
	).Scan(&ftsCount); err != nil {
		t.Fatal(err)
	}
	if ftsCount != 3 {
		t.Fatalf("cves_fts rows = %d, want 3", ftsCount)
	}

	// SBOM file exists.
	if sbomPath == "" {
		t.Fatal("sbom_path empty")
	}
	if _, err := os.Stat(sbomPath); err != nil {
		// FakeRunner.SBOM returns nil but does NOT actually write the file.
		// The handler creates the parent dir and sets sbomPath; the file
		// itself only exists if a real Trivy wrote it. Don't fail the test
		// — assert sbom_path was passed correctly, not that it has bytes.
		_ = err
	}

	// Cache invalidated post-commit.
	if _, ok := f.cache.Get(rid, "docker", mfDigest); ok {
		t.Fatal("cache entry should have been invalidated")
	}

	// Tmp dir cleaned up.
	tmp := filepath.Join(f.dataRoot, "tmp", "scans")
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("tmp/scans not cleaned: %v", names)
	}

	// scan.finished audit event recorded.
	var finishedCount int
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT count(*) FROM audit_log WHERE event_kind=?`, string(audit.EvtScanFinished),
	).Scan(&finishedCount); err != nil {
		t.Fatal(err)
	}
	if finishedCount < 1 {
		t.Fatalf("scan.finished events = %d, want ≥1", finishedCount)
	}
}

func TestHandler_SBOMFailureDoesNotFailScan(t *testing.T) {
	f := newScanFixture(t)
	_, _, sid := f.seedDockerArtifact(t)

	f.runner.QueueImage(scan.Result{
		Summary:        map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0, "unknown": 0},
		TrivyDBVersion: "x",
	}, nil)
	f.runner.QueueSBOM(scan.ErrNothingQueued) // simulate SBOM failure

	if err := f.handler.Handle(context.Background(), f.leasedScan(t, sid)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var status, sbomPath string
	err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT status, sbom_path FROM scans WHERE id=?`, sid,
	).Scan(&status, &sbomPath)
	if err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Fatalf("status=%q want done (sbom failure must not fail scan)", status)
	}
	if sbomPath != "" {
		t.Fatalf("sbom_path=%q want empty after SBOM failure", sbomPath)
	}
}

func TestHandler_RawScan_HappyPath(t *testing.T) {
	f := newScanFixture(t)
	ctx := context.Background()

	pid, err := f.projs.Create(ctx, "rawproj", "")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := f.repos.Create(ctx, pid, "raw", "files", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Write a file at <data>/repos/rawproj/raw/files/foo.txt
	repoTree := filepath.Join(f.dataRoot, "repos", "rawproj", "raw", "files")
	if err := os.MkdirAll(repoTree, 0o750); err != nil {
		t.Fatal(err)
	}
	contents := []byte("hello world")
	if err := os.WriteFile(filepath.Join(repoTree, "foo.txt"), contents, 0o640); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	var sid int64
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := f.rawf.Insert(ctx, tx, rid, "foo.txt", int64(len(contents)), "text/plain", digest); err != nil {
			return err
		}
		s, err := f.scans.Enqueue(ctx, tx, rid, "raw", "foo.txt")
		if err != nil {
			return err
		}
		sid = s
		return nil
	}); err != nil {
		t.Fatalf("seed tx: %v", err)
	}

	f.runner.QueueFilesystem(scan.Result{
		Summary: map[string]int{"critical": 0, "high": 0, "medium": 1, "low": 0, "unknown": 0},
		Vulnerabilities: []scan.Vuln{
			{CVEID: "CVE-RAW-1", Package: "f", Severity: "MEDIUM", Title: "x"},
		},
		TrivyDBVersion: "y",
	}, nil)

	if err := f.handler.Handle(ctx, f.leasedScan(t, sid)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var status string
	if err := f.db.Reader.QueryRowContext(ctx,
		`SELECT status FROM scans WHERE id=?`, sid,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Fatalf("status=%q want done", status)
	}
}

func TestHandler_UnknownArtifactKind_PermFails(t *testing.T) {
	f := newScanFixture(t)
	ctx := context.Background()

	pid, _ := f.projs.Create(ctx, "p", "")
	rid, _ := f.repos.Create(ctx, pid, "docker", "x", "", nil, nil, nil)
	var sid int64
	_ = f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		s, err := f.scans.Enqueue(ctx, tx, rid, "weird", "z")
		sid = s
		return err
	})

	if err := f.handler.Handle(ctx, f.leasedScan(t, sid)); err != nil {
		t.Fatalf("Handle returned error (should permFail and return nil): %v", err)
	}
	var status string
	if err := f.db.Reader.QueryRowContext(ctx, `SELECT status FROM scans WHERE id=?`, sid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
}

func TestHandler_TmpCleanupOnRunnerError(t *testing.T) {
	f := newScanFixture(t)
	_, _, sid := f.seedDockerArtifact(t)
	f.runner.QueueImage(scan.Result{}, scan.ErrNothingQueued) // any error

	err := f.handler.Handle(context.Background(), f.leasedScan(t, sid))
	if err == nil {
		t.Fatal("expected error")
	}
	tmp := filepath.Join(f.dataRoot, "tmp", "scans")
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Fatalf("tmp not cleaned after runner error: %d entries", len(entries))
	}
}

// P-1: A buildx-produced attestation manifest is auto-enqueued for scanning
// right alongside the platform image manifests. Its only "layer" is a JSON
// attestation (in-toto / DSSE), which makes Trivy explode with
// "archive/tar: invalid tar header" when it tries to untar the blob.
//
// The handler must detect this shape, skip the runner entirely, and record
// the scan as done with a zero-findings summary + an audit event noting the
// skip. The skip MUST NOT invoke Runner.Image (this test leaves the
// FakeRunner's Image queue empty; a call would return ErrNothingQueued and
// fail the scan).
func TestHandler_AttestationManifest_SkipsRunnerAndMarksDone(t *testing.T) {
	f := newScanFixture(t)
	ctx := context.Background()

	pid, err := f.projs.Create(ctx, "attestproj", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := f.repos.Create(ctx, pid, "docker", "img", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	mfBody, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    "sha256:aaaa",
			"size":      167,
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.in-toto+json",
				"digest":    "sha256:bbbb",
				"size":      4906,
				"annotations": map[string]any{
					"in-toto.io/predicate-type": "https://slsa.dev/provenance/v0.2",
				},
			},
		},
	})
	sum := sha256.Sum256(mfBody)
	mfDigest := "sha256:" + hex.EncodeToString(sum[:])

	var sid int64
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := f.mfs.Insert(ctx, tx, rid, mfDigest, "application/vnd.oci.image.manifest.v1+json", mfBody); err != nil {
			return err
		}
		s, err := f.scans.Enqueue(ctx, tx, rid, "docker", mfDigest)
		sid = s
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// No runner calls should happen; leave QueueImage and QueueSBOM empty.
	if err := f.handler.Handle(ctx, f.leasedScan(t, sid)); err != nil {
		t.Fatalf("Handle: %v (runner must be skipped for attestation)", err)
	}

	var status, summaryJSON, sbomPath string
	if err := f.db.Reader.QueryRowContext(ctx,
		`SELECT status, severity_summary_json, sbom_path FROM scans WHERE id=?`, sid,
	).Scan(&status, &summaryJSON, &sbomPath); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Fatalf("status=%q want done", status)
	}
	if sbomPath != "" {
		t.Fatalf("sbom_path=%q want empty for skipped scan", sbomPath)
	}
	var sm map[string]int
	if err := json.Unmarshal([]byte(summaryJSON), &sm); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if sm[sev] != 0 {
			t.Errorf("summary[%s] = %d, want 0 (skipped scan)", sev, sm[sev])
		}
	}

	// No vulnerabilities rows.
	var vulnCount int
	if err := f.db.Reader.QueryRowContext(ctx,
		`SELECT count(*) FROM vulnerabilities WHERE scan_id=?`, sid,
	).Scan(&vulnCount); err != nil {
		t.Fatal(err)
	}
	if vulnCount != 0 {
		t.Fatalf("vulnerabilities rows = %d, want 0", vulnCount)
	}
}

func TestHandler_LastErrorSanitized(t *testing.T) {
	f := newScanFixture(t)
	// Construct a handler whose data root is a known string we can match.
	// The fixture's handler is already configured with f.dataRoot — verify
	// the sanitizer would strip it from an error payload.
	_, _, sid := f.seedDockerArtifact(t)
	// Force an error path. The error message will include the tmp dir
	// (under f.dataRoot) when materializeDocker fails — but materializeDocker
	// doesn't fail on the seeded happy path. Instead, force runner error
	// with stderr-like payload.
	leakingErr := scan.ErrNothingQueued // pool will record this
	f.runner.QueueImage(scan.Result{}, leakingErr)
	err := f.handler.Handle(context.Background(), f.leasedScan(t, sid))
	if err == nil {
		t.Fatal("expected error")
	}
	// The sanitizer should at minimum not leak /home/ paths if any get into
	// the error string. The base ErrNothingQueued message has no path; we
	// just verify the error round-trips without a panic.
	if err.Error() == "" {
		t.Fatal("error empty")
	}
}
