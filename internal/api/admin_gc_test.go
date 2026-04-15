package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// newGCRESTServer wires api.Mount with GCDeps populated.
func newGCRESTServer(t *testing.T) *testServer {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	for _, d := range []string{"certs", "repos", "trash", "tmp", "logs", "sboms"} {
		if err := os.MkdirAll(filepath.Join(dataRoot, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	auditLogger, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	holder := omrtls.NewCertHolder()
	certPEM, keyPEM, err := omrtls.GenerateSelfSigned([]string{"localhost"}, time.Hour, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Swap(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	deps := api.Deps{
		DB:       db,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Repos:    metadata.NewReposRepo(db),
		Settings: metadata.NewSettingsRepo(db),
		Holder:   holder,
		DataRoot: dataRoot,
		Audit:    auditLogger,
		Trash:    storage.NewTrash(filepath.Join(dataRoot, "trash")),
		Locks:    storage.NewLocks(),
		GCDeps: &api.GCDeps{
			SyncJobs: metadata.NewSyncJobsRepo(db),
			SyncKick: func() {},
		},
	}
	mux := chi.NewRouter()
	mux.Get("/healthz", httpx.Healthz())
	api.Mount(mux, deps)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &testServer{mux: mux, ts: ts, db: db, deps: deps, dataRoot: dataRoot}
}

// TestAdminGC_SuperAdmin_Enqueues202 asserts a super-admin can trigger GC
// and the response is 202 with a valid job id pointing at a pending row.
func TestAdminGC_SuperAdmin_Enqueues202(t *testing.T) {
	s := newGCRESTServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	resp, body := s.do(t, "POST", "/api/v1/admin/gc", cookie, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("gc code=%d body=%v", resp.StatusCode, body)
	}
	jobIDF, ok := body["job_id"].(float64)
	if !ok || jobIDF == 0 {
		t.Fatalf("job_id missing: %v", body)
	}
	jobID := int64(jobIDF)

	var kind, status string
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT kind, status FROM sync_jobs WHERE id=?`, jobID).Scan(&kind, &status); err != nil {
		t.Fatal(err)
	}
	if kind != jobs.GCJobKind {
		t.Fatalf("kind=%s want %s", kind, jobs.GCJobKind)
	}
	if status != "pending" {
		t.Fatalf("status=%s want pending", status)
	}

	// Audit gc.triggered recorded.
	var c int
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind=?`, string(audit.EvtGCTriggered),
	).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatalf("gc.triggered audit rows = %d want 1", c)
	}
}

// TestAdminGC_NonSuperAdmin_403 asserts a plain user cannot trigger GC.
func TestAdminGC_NonSuperAdmin_403(t *testing.T) {
	s := newGCRESTServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, code := s.login(t, "alice", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	resp, _ := s.do(t, "POST", "/api/v1/admin/gc", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}
}

// TestAdminGC_Unauthenticated_401 asserts the endpoint requires auth.
func TestAdminGC_Unauthenticated_401(t *testing.T) {
	s := newGCRESTServer(t)
	resp, _ := s.do(t, "POST", "/api/v1/admin/gc", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", resp.StatusCode)
	}
}

// TestAdminGC_AlreadyRunning_409 asserts a second enqueue while a GC row
// is pending returns 409 already_running.
func TestAdminGC_AlreadyRunning_409(t *testing.T) {
	s := newGCRESTServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Insert a pending gc row directly.
	syncRepo := metadata.NewSyncJobsRepo(s.db)
	if err := s.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := syncRepo.Enqueue(context.Background(), tx, jobs.GCJobKind, 0, 0, "{}")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "POST", "/api/v1/admin/gc", cookie, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("code=%d want 409 body=%v", resp.StatusCode, body)
	}
}
