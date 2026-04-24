package pypi_test

// v1.5 Phase 3 Plan 03 — PYPIFIX-04 mirror-sync skip-continue integration test.
//
// TestSyncHandler_SkipsMalformedFilename (D-20) feeds the sync loop a PEP 691
// project index with one PEP-440-valid file + one malformed-version file
// and asserts:
//   (a) the valid file is fetched and inserted into pypi_files,
//   (b) no on-disk blob exists for the malformed filename (parse-before-
//       download invariant held — D-10),
//   (c) exactly one sync.file_skipped audit row lands with the expected
//       details_json shape {filename, reason, protocol, upstream_url, repo_id},
//   (d) the sync job ends with a sync.finished audit row (not sync.failed);
//       Handle returns nil.

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
	"testing"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

func TestPyPISyncJobKindStable(t *testing.T) {
	if pypi.SyncJobKind != "pypi_sync" {
		t.Fatalf("SyncJobKind = %q, want pypi_sync", pypi.SyncJobKind)
	}
}

// TestSyncHandler_SkipsMalformedFilename — PYPIFIX-04 (D-20). Upstream
// serves one valid + one malformed filename; the malformed one must be
// skipped via EvtSyncFileSkipped without failing the sync, and the valid
// one must land in pypi_files + on disk.
func TestSyncHandler_SkipsMalformedFilename(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	pypiFiles := metadata.NewPyPIFilesRepo(db)
	scans := metadata.NewScansRepo(db)

	// Two sdist payloads — byte content doesn't need to be a real tar.gz;
	// the sync handler hashes it and stores it as an opaque blob.
	bodyGood := []byte("sdist-bytes-goodpkg-1.0.0")
	bodyBad := []byte("sdist-bytes-badpkg-2do")
	dGood := pypiSkipHex(bodyGood)
	dBad := pypiSkipHex(bodyBad)

	badFetched := false
	mux := http.NewServeMux()
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		// Two distinct projects so each parses cleanly in the collect pass.
		_, _ = w.Write([]byte(`{"meta":{"api-version":"1.0"},"projects":[{"name":"goodpkg"},{"name":"badpkg"}]}`))
	})
	goodProjectJSON := fmt.Sprintf(`{
		"meta": {"api-version":"1.0"},
		"name":"goodpkg",
		"files":[
			{"filename":"goodpkg-1.0.0.tar.gz","url":"/packages/goodpkg-1.0.0.tar.gz","hashes":{"sha256":"%s"},"size":%d}
		]
	}`, dGood, len(bodyGood))
	// "badpkg-2do.tar.gz" — the only -<digit> boundary yields candidate
	// "2do" which fails pep440.Validate (see parse_filename_internal_test.go
	// "foo-2do" negative row from Plan 02). Sync must skip, not fail.
	badProjectJSON := fmt.Sprintf(`{
		"meta": {"api-version":"1.0"},
		"name":"badpkg",
		"files":[
			{"filename":"badpkg-2do.tar.gz","url":"/packages/badpkg-2do.tar.gz","hashes":{"sha256":"%s"},"size":%d}
		]
	}`, dBad, len(bodyBad))
	mux.HandleFunc("/simple/goodpkg/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(goodProjectJSON))
	})
	mux.HandleFunc("/simple/badpkg/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(badProjectJSON))
	})
	mux.HandleFunc("/packages/goodpkg-1.0.0.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bodyGood)
	})
	mux.HandleFunc("/packages/badpkg-2do.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		// D-10 parse-before-download invariant: the malformed filename
		// must never trigger a package GET. If this handler fires, the
		// skip-at-collect-pass path regressed.
		badFetched = true
		_, _ = w.Write(bodyBad)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "skippkg", "phase 03-03 skip test")
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

	// Real audit sink backed by sqlitetest DB so we can assert audit_log
	// rows by event_kind + details_json contents.
	auditPath := filepath.Join(dataRoot, "logs", "audit.log")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
		t.Fatal(err)
	}
	auditLog, err := audit.New(db, auditPath, 10, 1)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}

	h := pypi.NewSyncHandler(pypi.SyncDeps{
		DB:         db,
		Path:       pathStore,
		PyPIFiles:  pypiFiles,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		Audit:      auditLog,
		HTTPClient: srv.Client(),
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})

	jobID := seedPyPISyncJob(t, db, rid)
	payload := map[string]any{"upstream_url": srv.URL}
	pb, _ := json.Marshal(payload)

	// Assertion (d-part-one): Handle must return nil. The malformed file
	// triggers a skip, not a whole-sync failure.
	if err := h.Handle(ctx, string(pb), 0, rid, jobID); err != nil {
		t.Fatalf("Handle returned error (expected nil — skip should not fail sync): %v", err)
	}

	// Assertion (b): D-10 parse-before-download — no package GET for the
	// malformed filename.
	if badFetched {
		t.Fatalf("malformed filename was fetched from upstream — parse-before-download regressed (D-10)")
	}

	// Assertion (a): exactly one pypi_files row (the valid goodpkg file).
	goodRows, err := pypiFiles.ListByProject(ctx, rid, "goodpkg")
	if err != nil {
		t.Fatalf("list goodpkg files: %v", err)
	}
	if len(goodRows) != 1 {
		t.Fatalf("goodpkg pypi_files rows = %d, want 1", len(goodRows))
	}
	if goodRows[0].Filename != "goodpkg-1.0.0.tar.gz" {
		t.Errorf("goodpkg row filename = %q, want goodpkg-1.0.0.tar.gz", goodRows[0].Filename)
	}
	if goodRows[0].Version != "1.0.0" {
		t.Errorf("goodpkg row version = %q, want 1.0.0", goodRows[0].Version)
	}

	// No badpkg rows.
	badRows, err := pypiFiles.ListByProject(ctx, rid, "badpkg")
	if err != nil {
		t.Fatalf("list badpkg files: %v", err)
	}
	if len(badRows) != 0 {
		t.Fatalf("badpkg pypi_files rows = %d, want 0 (malformed filename must not land)", len(badRows))
	}

	// Assertion (b-part-two): no on-disk blob for malformed filename.
	badBlobPath := filepath.Join(repoRoot, "skippkg", "pypi", "r1", "packages", "badpkg-2do.tar.gz")
	if _, statErr := os.Stat(badBlobPath); !os.IsNotExist(statErr) {
		t.Fatalf("on-disk blob for malformed filename exists at %s (statErr=%v) — orphan blob regression", badBlobPath, statErr)
	}

	// Good blob should exist.
	goodBlobPath := filepath.Join(repoRoot, "skippkg", "pypi", "r1", "packages", "goodpkg-1.0.0.tar.gz")
	if _, statErr := os.Stat(goodBlobPath); statErr != nil {
		t.Fatalf("on-disk blob for good filename missing at %s: %v", goodBlobPath, statErr)
	}

	// Assertion (c): exactly one sync.file_skipped audit row with the
	// expected details_json shape.
	var skipCount int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE event_kind='sync.file_skipped' AND target_id=?`,
		fmt.Sprintf("%d", rid),
	).Scan(&skipCount); err != nil {
		t.Fatalf("count sync.file_skipped: %v", err)
	}
	if skipCount != 1 {
		t.Fatalf("sync.file_skipped rows = %d, want 1", skipCount)
	}

	var skipDetailsJSON string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT details_json FROM audit_log WHERE event_kind='sync.file_skipped' AND target_id=?`,
		fmt.Sprintf("%d", rid),
	).Scan(&skipDetailsJSON); err != nil {
		t.Fatalf("scan sync.file_skipped details: %v", err)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(skipDetailsJSON), &details); err != nil {
		t.Fatalf("details_json not valid JSON: %v", err)
	}
	// Required keys per D-12.
	if got := details["filename"]; got != "badpkg-2do.tar.gz" {
		t.Errorf("details.filename = %v, want badpkg-2do.tar.gz", got)
	}
	if got := details["reason"]; got != "pep440_invalid" {
		t.Errorf("details.reason = %v, want pep440_invalid", got)
	}
	if got := details["protocol"]; got != "pypi" {
		t.Errorf("details.protocol = %v, want pypi", got)
	}
	if upstreamURL, _ := details["upstream_url"].(string); upstreamURL == "" {
		t.Errorf("details.upstream_url empty; want non-empty URL for /packages/badpkg-2do.tar.gz")
	}
	// repo_id round-trips as float64 through json.Unmarshal into any;
	// tolerate both numeric types for forward-compatibility.
	if repoIDAny, ok := details["repo_id"]; !ok {
		t.Errorf("details.repo_id missing")
	} else {
		gotStr := fmt.Sprintf("%v", repoIDAny)
		wantStr := fmt.Sprintf("%d", rid)
		// json numbers come back as float64; "%v" renders as e.g. "1".
		// Compare via string form to dodge type.
		if gotStr != wantStr && gotStr != wantStr+"" {
			t.Errorf("details.repo_id = %v (%T), want %d", repoIDAny, repoIDAny, rid)
		}
	}

	// Assertion (d-part-two): sync.finished emitted (not sync.failed).
	var finishedCount, failedCount int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE event_kind='sync.finished' AND target_id=?`,
		fmt.Sprintf("%d", rid),
	).Scan(&finishedCount); err != nil {
		t.Fatalf("count sync.finished: %v", err)
	}
	if finishedCount != 1 {
		t.Fatalf("sync.finished rows = %d, want 1", finishedCount)
	}
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE event_kind='sync.failed' AND target_id=?`,
		fmt.Sprintf("%d", rid),
	).Scan(&failedCount); err != nil {
		t.Fatalf("count sync.failed: %v", err)
	}
	if failedCount != 0 {
		t.Fatalf("sync.failed rows = %d, want 0 (skip must not fail the sync)", failedCount)
	}

	// sync_jobs table row reflects a non-running final state as well.
	// current_step is set to "done" after Handle completes, and
	// progress_bytes reflects actual downloaded good-file bytes.
	//
	// Note on total_bytes: the collect pass (sync_handler.go lines
	// ~172-213) sums UpstreamFile.Size for every file that passes
	// isInstallableExt + isSafeMirrorFilename + idempotency — the parse
	// step runs INSIDE fetchAndCommit (post-collect), so total_bytes
	// includes the malformed file's Size even though no bytes ever flow
	// across the wire for it. progress_bytes therefore ends BELOW
	// total_bytes on skip-partial syncs (progress = sum of good-file
	// bytes; total = sum of all-in-scope sizes). This is the real
	// observed shape of the sync_jobs row after a partial-skip sync —
	// narrowing the total to post-parse files would require moving the
	// parse into the collect pass (deferred: current parse-in-download
	// seam keeps fetchAndCommit the single parse authority and avoids
	// duplicate parse calls).
	var progressBytes, totalBytes int64
	var currentStep string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&progressBytes, &totalBytes, &currentStep); err != nil {
		t.Fatalf("scan sync_jobs: %v", err)
	}
	if progressBytes != int64(len(bodyGood)) {
		t.Errorf("progress_bytes = %d, want %d (goodpkg downloaded bytes)", progressBytes, len(bodyGood))
	}
	if totalBytes != int64(len(bodyGood)+len(bodyBad)) {
		t.Errorf("total_bytes = %d, want %d (collect-pass sums all in-scope file sizes; parse skip happens later)",
			totalBytes, len(bodyGood)+len(bodyBad))
	}
	if currentStep != "done" {
		t.Errorf("current_step = %q, want \"done\" after successful (partial-skip) sync", currentStep)
	}
}

func pypiSkipHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
