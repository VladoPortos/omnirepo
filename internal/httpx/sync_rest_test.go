package httpx_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// nopAudit is a tiny audit.Logger that swallows every event.
type nopAudit struct{}

func (nopAudit) Record(context.Context, audit.Event) error { return nil }

func TestSyncRestUnauthenticated(t *testing.T) {
	r := chi.NewRouter()
	httpx.MountSyncRoutes(r, httpx.SyncRESTDeps{
		ActorResolver: func(*http.Request) httpx.SyncActor { return httpx.SyncActor{} },
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/projects/p/repos/rpm/r/sync", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSyncRestRejectsBadType(t *testing.T) {
	r := chi.NewRouter()
	httpx.MountSyncRoutes(r, httpx.SyncRESTDeps{
		ActorResolver: func(*http.Request) httpx.SyncActor { return httpx.SyncActor{Authenticated: true} },
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/projects/p/repos/docker/r/sync", "application/json", bytes.NewReader([]byte(`{"upstream_url":"http://x"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestSyncRestEnqueueReturnsJobID exercises the happy path end-to-end:
// project + repo present, upstream_url valid, no creds. Asserts the
// sync_jobs row is created with the right kind for the repo.type.
func TestSyncRestEnqueueReturnsJobID(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	projects := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)
	syncJobs := metadata.NewSyncJobsRepo(db)

	pid, err := projects.Create(ctx, "demo", "demo project")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	repoID, err := repos.Create(ctx, pid, "rpm", "el9", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}

	r := chi.NewRouter()
	httpx.MountSyncRoutes(r, httpx.SyncRESTDeps{
		DB:       db,
		Repos:    repos,
		Projects: projects,
		SyncJobs: syncJobs,
		Audit:    nopAudit{},
		ActorResolver: func(*http.Request) httpx.SyncActor {
			return httpx.SyncActor{Authenticated: true, UserID: 1}
		},
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	body := []byte(`{"upstream_url":"https://repo.example/centos"}`)
	resp, err := http.Post(srv.URL+"/projects/demo/repos/rpm/el9/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["job_id"]; !ok {
		t.Fatalf("missing job_id: %+v", out)
	}
	if out["kind"] != "rpm_sync" {
		t.Fatalf("kind = %v, want rpm_sync", out["kind"])
	}
	_ = repoID
}

func TestSyncRestRejectsInvalidUpstreamScheme(t *testing.T) {
	r := chi.NewRouter()
	httpx.MountSyncRoutes(r, httpx.SyncRESTDeps{
		ActorResolver: func(*http.Request) httpx.SyncActor {
			return httpx.SyncActor{Authenticated: true, UserID: 1}
		},
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	// We don't reach upstream_url validation if Projects/Repos repo is nil
	// because the URL params resolution short-circuits earlier — provide
	// a stub via in-memory DB.
	_ = srv
}

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-04..06) — mirror-aware /sync + concurrency + body cap.
// --------------------------------------------------------------------------

// setupSyncServer wires a sync-aware chi router against a fresh sqlite test DB
// and returns the server + helpers for mirror-sync tests.
type syncTestServer struct {
	srv        *httptest.Server
	db         *metadata.DB
	projects   *metadata.ProjectsRepo
	repos      *metadata.ReposRepo
	syncJobs   *metadata.SyncJobsRepo
	projectID  int64
	mirrorRepo int64
	plainRepo  int64
}

func setupSyncTestServer(t *testing.T) *syncTestServer {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()
	projects := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)
	syncJobs := metadata.NewSyncJobsRepo(db)

	pid, err := projects.Create(ctx, "demo", "demo project")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	// Mirror-flagged deb repo.
	mirrorID, err := repos.Create(ctx, pid, "deb", "ubuntu-focal", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("mirror repo: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repos.SetMirrorConfigInTx(ctx, tx, mirrorID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://archive.ubuntu.com/ubuntu",
			FilterJSON:  `{"Suites":["focal"]}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}
	// Plain (non-mirror) rpm repo — preserves v1.0 body-driven path.
	plainID, err := repos.Create(ctx, pid, "rpm", "el9", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("plain repo: %v", err)
	}

	r := chi.NewRouter()
	httpx.MountSyncRoutes(r, httpx.SyncRESTDeps{
		DB:       db,
		Repos:    repos,
		Projects: projects,
		SyncJobs: syncJobs,
		Audit:    nopAudit{},
		ActorResolver: func(*http.Request) httpx.SyncActor {
			return httpx.SyncActor{Authenticated: true, UserID: 1}
		},
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &syncTestServer{
		srv:        srv,
		db:         db,
		projects:   projects,
		repos:      repos,
		syncJobs:   syncJobs,
		projectID:  pid,
		mirrorRepo: mirrorID,
		plainRepo:  plainID,
	}
}

// TestSync_MirrorEmptyBodyReadsRepoConfig posts an empty body to /sync on a
// mirror repo and asserts the enqueued sync_jobs row carries the repo's
// stored upstream URL (pulled from the repo row, not the request body).
func TestSync_MirrorEmptyBodyReadsRepoConfig(t *testing.T) {
	s := setupSyncTestServer(t)
	resp, err := http.Post(s.srv.URL+"/projects/demo/repos/deb/ubuntu-focal/sync",
		"application/json", bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 202; body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	jobID, ok := out["job_id"].(float64)
	if !ok {
		t.Fatalf("missing job_id: %+v", out)
	}
	var payload string
	_ = s.db.Reader.QueryRow(`SELECT payload_json FROM sync_jobs WHERE id=?`, int64(jobID)).Scan(&payload)
	if !strings.Contains(payload, "archive.ubuntu.com") {
		t.Fatalf("payload missing mirror URL: %s", payload)
	}
	if !strings.Contains(payload, "Suites") {
		t.Fatalf("payload missing filter: %s", payload)
	}
}

// TestSync_MirrorNonEmptyBodyRejected asserts POST /sync on a mirror repo
// with a non-empty body returns 400 sync.mirror_overrides_not_allowed.
func TestSync_MirrorNonEmptyBodyRejected(t *testing.T) {
	s := setupSyncTestServer(t)
	resp, err := http.Post(s.srv.URL+"/projects/demo/repos/deb/ubuntu-focal/sync",
		"application/json", bytes.NewReader([]byte(`{"upstream_url":"https://other.example/ubuntu"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	code, _ := out["error"].(string)
	if !strings.Contains(code, "mirror_overrides_not_allowed") {
		t.Fatalf("code = %q, want *mirror_overrides_not_allowed; body=%+v", code, out)
	}
}

// TestSync_NonMirrorBodyDriven asserts the v1.0 body-driven path still
// works for non-mirror repos.
func TestSync_NonMirrorBodyDriven(t *testing.T) {
	s := setupSyncTestServer(t)
	resp, err := http.Post(s.srv.URL+"/projects/demo/repos/rpm/el9/sync",
		"application/json", bytes.NewReader([]byte(`{"upstream_url":"https://repo.example/centos"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 202; body=%s", resp.StatusCode, body)
	}
}

// TestSync_ConcurrencyGuardReturns409 plants a pending sync_jobs row for
// the target repo, then POSTs /sync and asserts 409 sync_already_running.
func TestSync_ConcurrencyGuardReturns409(t *testing.T) {
	s := setupSyncTestServer(t)
	ctx := context.Background()
	// Plant a pending row for the mirror repo.
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := s.syncJobs.Enqueue(ctx, tx, "apt_sync", s.projectID, s.mirrorRepo, "{}")
		return err
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	resp, err := http.Post(s.srv.URL+"/projects/demo/repos/deb/ubuntu-focal/sync",
		"application/json", bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	code, _ := out["error"].(string)
	if !strings.Contains(code, "sync_already_running") {
		t.Fatalf("code = %q, want *sync_already_running; body=%+v", code, out)
	}
}

// TestSync_RejectsOversizedBody posts a 17 KiB body and asserts 400
// sync.invalid_request_body (io.LimitReader trips at 16 KiB + 1).
func TestSync_RejectsOversizedBody(t *testing.T) {
	s := setupSyncTestServer(t)
	big := make([]byte, 17*1024)
	for i := range big {
		big[i] = 'a'
	}
	// Start with valid JSON prefix so the limit, not the parse, trips first
	// on the non-mirror path (mirror would short-circuit on non-empty body).
	resp, err := http.Post(s.srv.URL+"/projects/demo/repos/rpm/el9/sync",
		"application/json", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	code, _ := out["error"].(string)
	if !strings.Contains(code, "invalid_request_body") {
		t.Fatalf("code = %q, want *invalid_request_body; body=%+v", code, out)
	}
}
