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
