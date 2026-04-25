package api_test

// Phase 8 Plan 02 (M2.2) — sync-job GET endpoint tests covering the
// progress_bytes / total_bytes / current_step triple added by this plan.
//
// The public-facing endpoint is
//   GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}
// (the design spec refers to it as "GET /api/v1/jobs/{id}" for brevity;
// in this codebase the route is scoped under the owning project/repo
// pair per Phase 05-04 SYNC-06).
//
// Deviation from plan 08-02 Task 2: tests live here rather than in
// admin_jobs_test.go because admin_jobs_test.go covers a different
// endpoint (GET /api/v1/admin/jobs/summary, D-06) that does not return
// per-job rows. Placing the progress-field tests next to the list tests
// keeps them with the handler they exercise.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// seedSyncJob inserts a running sync_jobs row for repoID and returns its id.
func seedSyncJob(t *testing.T, db *metadata.DB, repoID int64, kind string) int64 {
	t.Helper()
	res, err := db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES (?, ?, 'running', '{}', '{}')`,
		kind, repoID,
	)
	if err != nil {
		t.Fatalf("seed sync_jobs row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// lookupRepoID reads the id of the (project, type, name) triple created by
// bootProjectAndRepo.
func lookupRepoID(t *testing.T, db *metadata.DB, project, typ, name string) int64 {
	t.Helper()
	var repoID int64
	err := db.Reader.QueryRowContext(context.Background(),
		`SELECT r.id FROM repos r
		  JOIN projects p ON p.id = r.project_id
		  WHERE p.name=? AND r.type=? AND r.name=?`,
		project, typ, name,
	).Scan(&repoID)
	if err != nil {
		t.Fatalf("lookup repo id: %v", err)
	}
	return repoID
}

// TestGetSyncJob_IncludesProgressFields seeds a sync_jobs row, calls
// SetProgress with a concrete triple, and asserts the GET handler
// serializes all three values unchanged.
func TestGetSyncJob_IncludesProgressFields(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pp", "deb", "r1")
	repoID := lookupRepoID(t, s.db, "pp", "deb", "r1")

	jobID := seedSyncJob(t, s.db, repoID, "apt_sync")

	jobsRepo := metadata.NewSyncJobsRepo(s.db)
	if err := jobsRepo.SetProgress(context.Background(), jobID, "layer 3 of 7", 42, 103); err != nil {
		t.Fatalf("SetProgress: %v", err)
	}

	path := "/api/v1/projects/pp/repos/deb/r1/sync-jobs/" + strconv.FormatInt(jobID, 10)
	resp, body := s.do(t, "GET", path, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET sync-job: code=%d body=%+v", resp.StatusCode, body)
	}

	if got, ok := body["progress_bytes"].(float64); !ok || int64(got) != 42 {
		t.Errorf("progress_bytes=%v (%T); want 42", body["progress_bytes"], body["progress_bytes"])
	}
	if got, ok := body["total_bytes"].(float64); !ok || int64(got) != 103 {
		t.Errorf("total_bytes=%v (%T); want 103", body["total_bytes"], body["total_bytes"])
	}
	if got, ok := body["current_step"].(string); !ok || got != "layer 3 of 7" {
		t.Errorf("current_step=%v (%T); want 'layer 3 of 7'", body["current_step"], body["current_step"])
	}
}

// TestGetSyncJob_DefaultZeroValuesEmit asserts that a freshly-enqueued job
// (no SetProgress yet) serializes the three progress fields as their
// zero values (0 / 0 / "") rather than omitting them — so the UI renders
// a deterministic `0 / 0 bytes` at job start instead of `undefined`.
func TestGetSyncJob_DefaultZeroValuesEmit(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pp2", "pypi", "r1")
	repoID := lookupRepoID(t, s.db, "pp2", "pypi", "r1")

	jobID := seedSyncJob(t, s.db, repoID, "pypi_sync")

	path := "/api/v1/projects/pp2/repos/pypi/r1/sync-jobs/" + strconv.FormatInt(jobID, 10)
	resp, body := s.do(t, "GET", path, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET sync-job: code=%d body=%+v", resp.StatusCode, body)
	}

	if _, ok := body["progress_bytes"]; !ok {
		t.Errorf("response missing progress_bytes key; got %+v", body)
	}
	if got, ok := body["progress_bytes"].(float64); !ok || got != 0 {
		t.Errorf("progress_bytes=%v; want 0", body["progress_bytes"])
	}
	if got, ok := body["total_bytes"].(float64); !ok || got != 0 {
		t.Errorf("total_bytes=%v; want 0", body["total_bytes"])
	}
	if got, ok := body["current_step"].(string); !ok || got != "" {
		t.Errorf("current_step=%v; want empty string", body["current_step"])
	}
}

// TestListSyncJobs_IncludesProgressFields verifies the list endpoint (not
// just the by-id endpoint) also projects the 3 progress fields. UI
// timelines that show running + historical jobs in one pane depend on
// the list handler emitting the same shape.
func TestListSyncJobs_IncludesProgressFields(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "pp3", "helm", "r1")
	repoID := lookupRepoID(t, s.db, "pp3", "helm", "r1")

	jobID := seedSyncJob(t, s.db, repoID, "helm_sync")
	jobsRepo := metadata.NewSyncJobsRepo(s.db)
	if err := jobsRepo.SetProgress(context.Background(), jobID, "chart 2 of 12 · redis-17.0.0.tgz", 2, 0); err != nil {
		t.Fatalf("SetProgress: %v", err)
	}

	resp, body := s.do(t, "GET", "/api/v1/projects/pp3/repos/helm/r1/sync-jobs", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list code=%d body=%+v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) < 1 {
		t.Fatalf("items=%v; want >=1 entry", body["items"])
	}
	first := items[0].(map[string]any)
	if got, ok := first["progress_bytes"].(float64); !ok || int64(got) != 2 {
		t.Errorf("list[0].progress_bytes=%v; want 2", first["progress_bytes"])
	}
	if got, ok := first["total_bytes"].(float64); !ok || int64(got) != 0 {
		t.Errorf("list[0].total_bytes=%v; want 0 (Helm is step-based)", first["total_bytes"])
	}
	if got, ok := first["current_step"].(string); !ok || got != "chart 2 of 12 · redis-17.0.0.tgz" {
		t.Errorf("list[0].current_step=%q; want 'chart 2 of 12 · redis-17.0.0.tgz'", got)
	}
}

// TestGetSyncJob_IncludesFilesSynced — quick task 260420-d03 (D-03 closure).
//
// REST-layer regression per the filter.Suites pattern (D-29): the
// files_synced column is written by SyncJobsRepo.SetFilesSynced and must
// round-trip through both sync-jobs GET and list endpoints so the UI
// pill can render "Sync complete · N files · X MB". Unit tests that
// call the repo directly would miss a wire-shape drift here — this test
// hits the real REST surface.
func TestGetSyncJob_IncludesFilesSynced(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "dfs1", "pypi", "r1")
	repoID := lookupRepoID(t, s.db, "dfs1", "pypi", "r1")

	jobID := seedSyncJob(t, s.db, repoID, "pypi_sync")
	jobsRepo := metadata.NewSyncJobsRepo(s.db)
	if err := jobsRepo.SetFilesSynced(context.Background(), jobID, 7); err != nil {
		t.Fatalf("SetFilesSynced: %v", err)
	}

	// By-id endpoint.
	path := "/api/v1/projects/dfs1/repos/pypi/r1/sync-jobs/" + strconv.FormatInt(jobID, 10)
	resp, body := s.do(t, "GET", path, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET sync-job: code=%d body=%+v", resp.StatusCode, body)
	}
	if _, ok := body["files_synced"]; !ok {
		t.Errorf("response missing files_synced key; got %+v", body)
	}
	if got, ok := body["files_synced"].(float64); !ok || int64(got) != 7 {
		t.Errorf("files_synced=%v (%T); want 7", body["files_synced"], body["files_synced"])
	}

	// List endpoint — UI timelines depend on the same shape on both.
	resp, body = s.do(t, "GET", "/api/v1/projects/dfs1/repos/pypi/r1/sync-jobs", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list code=%d body=%+v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) < 1 {
		t.Fatalf("items=%v; want >=1 entry", body["items"])
	}
	first := items[0].(map[string]any)
	if got, ok := first["files_synced"].(float64); !ok || int64(got) != 7 {
		t.Errorf("list[0].files_synced=%v; want 7", first["files_synced"])
	}
}

// TestGetSyncJob_IncludesSummary — UIBACK-01 (v1.7).
//
// SyncJobsRepo.SetSummaryDriftPurged json_set's a `drift_purged` integer
// into sync_jobs.summary. The wire-shape contract: BOTH list and by-id
// endpoints emit the raw `summary` JSON string, COALESCEd to '{}' for
// rows the column-default touched but no writer has stamped. This test
// pins that contract end-to-end so a future schema change or COALESCE
// removal can't silently drop the summary column from the payload (the
// SyncHistoryDialog drift-purged sub-line depends on it).
func TestGetSyncJob_IncludesSummary(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "sumcase", "rpm", "r1")
	repoID := lookupRepoID(t, s.db, "sumcase", "rpm", "r1")

	jobID := seedSyncJob(t, s.db, repoID, "rpm_sync")
	jobsRepo := metadata.NewSyncJobsRepo(s.db)
	if err := jobsRepo.SetSummaryDriftPurged(context.Background(), jobID, 12); err != nil {
		t.Fatalf("SetSummaryDriftPurged: %v", err)
	}

	// By-id endpoint.
	path := "/api/v1/projects/sumcase/repos/rpm/r1/sync-jobs/" + strconv.FormatInt(jobID, 10)
	resp, body := s.do(t, "GET", path, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET sync-job: code=%d body=%+v", resp.StatusCode, body)
	}
	summary, ok := body["summary"].(string)
	if !ok {
		t.Fatalf("response missing summary string key; got %+v", body)
	}
	if !strings.Contains(summary, `"drift_purged":12`) {
		t.Errorf("summary=%q; want substring \"drift_purged\":12", summary)
	}

	// List endpoint — same shape contract.
	resp, body = s.do(t, "GET", "/api/v1/projects/sumcase/repos/rpm/r1/sync-jobs", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list code=%d body=%+v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) < 1 {
		t.Fatalf("items=%v; want >=1 entry", body["items"])
	}
	first := items[0].(map[string]any)
	listSummary, ok := first["summary"].(string)
	if !ok {
		t.Fatalf("list[0] missing summary string; got %+v", first)
	}
	if !strings.Contains(listSummary, `"drift_purged":12`) {
		t.Errorf("list[0].summary=%q; want substring \"drift_purged\":12", listSummary)
	}
}

// TestGetSyncJob_SummaryDefaultEmptyObject pins the COALESCE contract:
// a sync job that no writer has stamped still emits summary as the JSON
// string "{}" rather than omitting the key. This keeps the UI parser
// trivial (always JSON.parse-able, never undefined-checked).
func TestGetSyncJob_SummaryDefaultEmptyObject(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "sumdef", "helm", "r1")
	repoID := lookupRepoID(t, s.db, "sumdef", "helm", "r1")

	jobID := seedSyncJob(t, s.db, repoID, "helm_sync")

	path := "/api/v1/projects/sumdef/repos/helm/r1/sync-jobs/" + strconv.FormatInt(jobID, 10)
	resp, body := s.do(t, "GET", path, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET sync-job: code=%d body=%+v", resp.StatusCode, body)
	}
	summary, ok := body["summary"].(string)
	if !ok {
		t.Fatalf("response missing summary string key; got %+v", body)
	}
	if summary != "{}" {
		t.Errorf("summary=%q; want \"{}\"", summary)
	}
}

// TestGetSyncJob_FilesSyncedDefaultZero asserts a freshly-enqueued job
// (no SetFilesSynced yet) serializes files_synced as 0 rather than
// omitting the key — the UI relies on a deterministic cold-start frame
// to decide whether to render the "N files" piece of the pill.
func TestGetSyncJob_FilesSyncedDefaultZero(t *testing.T) {
	s := newTestServer(t)
	cookie := bootProjectAndRepo(t, s, "dfs2", "helm", "r1")
	repoID := lookupRepoID(t, s.db, "dfs2", "helm", "r1")

	jobID := seedSyncJob(t, s.db, repoID, "helm_sync")

	path := "/api/v1/projects/dfs2/repos/helm/r1/sync-jobs/" + strconv.FormatInt(jobID, 10)
	resp, body := s.do(t, "GET", path, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET sync-job: code=%d body=%+v", resp.StatusCode, body)
	}
	if _, ok := body["files_synced"]; !ok {
		t.Errorf("response missing files_synced key; got %+v", body)
	}
	if got, ok := body["files_synced"].(float64); !ok || got != 0 {
		t.Errorf("files_synced=%v; want 0", body["files_synced"])
	}
}
