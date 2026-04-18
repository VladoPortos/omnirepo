package api_test

// Phase 07 Plan 05 / D-06: GET /api/v1/admin/jobs/summary handler tests.
//
// The endpoint is super-admin-gated (RequireCan(ActionTriggerGC), reused
// per D-06 rather than introducing a new policy action) and returns the
// 5-key locked shape:
//     { running, queued, failed_last_24h, last_completed_at, last_failed_at }
//
// Three tests mirror admin_gc_test.go conventions:
//   - 200 with shape for a super-admin session that has seeded sync_jobs rows
//   - 403 for an authenticated non-super-admin
//   - 401 for an unauthenticated request
//
// Test server factory: `newGCRESTServer` already wires GCDeps+SyncJobs
// into api.Deps so admin_jobs reuses exactly the same in-process sync_jobs
// table (shared with admin_gc tests).

import (
	"context"
	"net/http"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/jobs"
)

// TestAdminJobsSummary_SuperAdmin_ReturnsShape seeds four sync_jobs rows
// (1 running + 1 pending + 1 failed-in-last-24h + 1 done-completed-now) and
// asserts the handler returns the D-06 shape with the expected counts +
// non-null last_completed_at.
func TestAdminJobsSummary_SuperAdmin_ReturnsShape(t *testing.T) {
	s := newGCRESTServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("admin login code=%d", code)
	}

	// Seed 4 sync_jobs rows spanning the counts under test.
	// (Direct inserts — we do NOT want the real pool leasing these rows.)
	ctx := context.Background()
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO sync_jobs(kind, status, payload_json, leased_at, log) VALUES (?, 'running', '{}', CURRENT_TIMESTAMP, '{}')`,
		jobs.GCJobKind,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO sync_jobs(kind, status, payload_json, log) VALUES (?, 'pending', '{}', '{}')`,
		jobs.GCJobKind,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO sync_jobs(kind, status, payload_json, leased_at, log, updated_at) VALUES (?, 'failed', '{}', CURRENT_TIMESTAMP, '{}', datetime('now','-1 hour'))`,
		jobs.GCJobKind,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Writer.ExecContext(ctx,
		`INSERT INTO sync_jobs(kind, status, payload_json, leased_at, log, updated_at) VALUES (?, 'done', '{}', CURRENT_TIMESTAMP, '{}', CURRENT_TIMESTAMP)`,
		jobs.GCJobKind,
	); err != nil {
		t.Fatal(err)
	}

	resp, body := s.do(t, "GET", "/api/v1/admin/jobs/summary", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}

	// D-06 shape: all 5 keys present (even when values are null).
	for _, k := range []string{"running", "queued", "failed_last_24h", "last_completed_at", "last_failed_at"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing key %q in response: %v", k, body)
		}
	}

	// Expected counts from the seeded rows.
	if got, _ := body["running"].(float64); got != 1 {
		t.Errorf("running=%v want 1", body["running"])
	}
	if got, _ := body["queued"].(float64); got != 1 {
		t.Errorf("queued=%v want 1", body["queued"])
	}
	if got, _ := body["failed_last_24h"].(float64); got != 1 {
		t.Errorf("failed_last_24h=%v want 1", body["failed_last_24h"])
	}

	// last_completed_at must be a non-null RFC3339 string because we inserted
	// a done row just now.
	if body["last_completed_at"] == nil {
		t.Errorf("last_completed_at=null; want RFC3339 string after inserting a done row")
	}
	// last_failed_at must also be non-null because we inserted a failed row
	// in the last 24h.
	if body["last_failed_at"] == nil {
		t.Errorf("last_failed_at=null; want RFC3339 string after inserting a failed row")
	}
}

// TestAdminJobsSummary_NonSuperAdmin_403 asserts a non-super-admin session
// is rejected at the policy gate (ActionTriggerGC reused per D-06).
func TestAdminJobsSummary_NonSuperAdmin_403(t *testing.T) {
	s := newGCRESTServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)
	resp, _ := s.do(t, "GET", "/api/v1/admin/jobs/summary", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}
}

// TestAdminJobsSummary_Unauthenticated_401 asserts the endpoint requires
// authentication (no session cookie → 401 from SessionOrAPIKey middleware).
func TestAdminJobsSummary_Unauthenticated_401(t *testing.T) {
	s := newGCRESTServer(t)
	resp, _ := s.do(t, "GET", "/api/v1/admin/jobs/summary", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", resp.StatusCode)
	}
}
