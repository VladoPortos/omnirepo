// Package api_test — black-box HTTP tests for POST /api/v1/admin/db/health/check
// (plan 10-03, DBHEALTH-05).
//
// These tests cover the manual-trigger endpoint's full contract:
//   - 202 Accepted on first call in the window.
//   - 429 rate_limited with details.retry_after_seconds + Retry-After header
//     when a previous run is <1h old.
//   - 409 already_running with details.job_started_at when a check is
//     in-flight.
//   - 403 permission for non-super-admin callers.
//   - admin.integrity_check.triggered audit event at POST entry.
//   - admin.integrity_check.completed emitted by the goroutine on success.
//   - Lease released on goroutine panic (Pitfall 10.4) — the subsequent
//     POST must NOT return 409.
//   - Sequential POST after the rate-limit window returns 202.
//
// White-box lease mutation + runner injection go through test hooks
// exported by admin_db_health_internal_test.go (ResetDBHealthJobForTest,
// SetDBHealthJobLastRunAtForTest, SetDBHealthJobRunningForTest,
// SetIntegrityCheckRunnerForTest, DBHealthJobStateForTest,
// DBHealthJobLastStatusForTest).
package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// doPostHealthCheck issues a POST /api/v1/admin/db/health/check as the
// given cookie and returns the response + parsed JSON body. Also returns
// the Retry-After header so rate-limit tests can assert on it.
func doPostHealthCheck(t *testing.T, s *testServer, cookie string) (
	status int, retryAfter string, body map[string]any,
) {
	t.Helper()
	resp, parsed := s.do(t, "POST", "/api/v1/admin/db/health/check", cookie, nil)
	return resp.StatusCode, resp.Header.Get("Retry-After"), parsed
}

// waitForLeaseIdle polls DBHealthJobStateForTest until it returns "idle"
// or the deadline elapses. Returns the final observed state. 50 ms poll
// cadence matches the plan's draft; 5 s deadline is generous (the PRAGMA
// on an empty in-memory DB completes in sub-millisecond time).
func waitForLeaseIdle(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if state := api.DBHealthJobStateForTest(); state == "idle" {
			return state
		}
		time.Sleep(50 * time.Millisecond)
	}
	return api.DBHealthJobStateForTest()
}

// countAuditRows returns the number of audit_log rows with the given
// event_kind. Uses s.db.Reader directly (same pattern as
// admin_gc_test.go) so results are read-after-write consistent with the
// strict DB insert path in audit.logger.Record.
func countAuditRows(t *testing.T, s *testServer, kind audit.EventKind) int {
	t.Helper()
	var n int
	if err := s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE event_kind=?`, string(kind),
	).Scan(&n); err != nil {
		t.Fatalf("count audit rows for %s: %v", kind, err)
	}
	return n
}

// -----------------------------------------------------------------------------
// Behavior tests
// -----------------------------------------------------------------------------

// TestAdminDBHealthCheck_202OnFirstCall — a never-run lease + super-admin
// caller must receive 202 with a parseable RFC3339 job_started_at. The
// goroutine is allowed to run to completion (waitForLeaseIdle) so the
// test does not bleed the lease into subsequent tests.
func TestAdminDBHealthCheck_202OnFirstCall(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	code, retryAfter, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusAccepted {
		t.Fatalf("code=%d want 202; body=%v", code, body)
	}
	if retryAfter != "" {
		t.Fatalf("Retry-After=%q on 202; want empty", retryAfter)
	}
	jobStartedAt, _ := body["job_started_at"].(string)
	if _, err := time.Parse(time.RFC3339, jobStartedAt); err != nil {
		t.Fatalf("job_started_at %q not RFC3339: %v", jobStartedAt, err)
	}

	// Let the goroutine finish so subsequent tests in the same binary run
	// on a clean lease. ResetDBHealthJobForTest's cleanup already restores
	// the zero value, but we still want the goroutine to not be racing
	// past the test boundary.
	if state := waitForLeaseIdle(t, 5*time.Second); state != "idle" {
		t.Fatalf("lease stuck at state=%q after 5s", state)
	}
}

// TestAdminDBHealthCheck_RateLimited_Returns429WithRetryAfter — simulate
// a manual run that completed 30 min ago (lastRunAt = now - 30 min). A
// fresh POST must return 429 with envelope code integrity_check.rate_limited,
// details.retry_after_seconds in (1700, 1800] seconds (30 min window), and
// a Retry-After HTTP header matching the same count.
func TestAdminDBHealthCheck_RateLimited_Returns429WithRetryAfter(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Seed a completed run 30 min ago.
	api.SetDBHealthJobLastRunAtForTest(t, time.Now().Add(-30*time.Minute))

	code, retryAfter, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429; body=%v", code, body)
	}
	if body["code"] != "integrity_check.rate_limited" {
		t.Fatalf("envelope code=%v want integrity_check.rate_limited; body=%v",
			body["code"], body)
	}
	if body["class"] != "transient" {
		t.Fatalf("envelope class=%v want transient", body["class"])
	}

	// Details block.
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing: %v", body["details"])
	}
	retrySecF, ok := details["retry_after_seconds"].(float64)
	if !ok {
		t.Fatalf("retry_after_seconds missing/not-number: %v", details["retry_after_seconds"])
	}
	retrySec := int(retrySecF)
	// 30-min window has ~1800 s left; allow slack for test wall-clock.
	if retrySec <= 1700 || retrySec > 1801 {
		t.Fatalf("retry_after_seconds=%d outside (1700, 1801]; 30min window should be ~1800",
			retrySec)
	}

	// Retry-After header must be present and match details.retry_after_seconds.
	if retryAfter == "" {
		t.Fatalf("Retry-After header missing on 429")
	}
	if retryAfter != itoa(int64(retrySec)) {
		t.Fatalf("Retry-After header=%q != details.retry_after_seconds=%d",
			retryAfter, retrySec)
	}
}

// TestAdminDBHealthCheck_AlreadyRunning_Returns409 — prime the lease into
// state="running" with a known startedAt. A POST must return 409 with
// envelope code integrity_check.already_running and
// details.job_started_at matching the seeded value.
func TestAdminDBHealthCheck_AlreadyRunning_Returns409(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	startedAt := time.Now().Add(-5 * time.Second)
	api.SetDBHealthJobRunningForTest(t, startedAt)

	code, retryAfter, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusConflict {
		t.Fatalf("code=%d want 409; body=%v", code, body)
	}
	if retryAfter != "" {
		t.Fatalf("Retry-After set on 409 (not rate-limit): %q", retryAfter)
	}
	if body["code"] != "integrity_check.already_running" {
		t.Fatalf("envelope code=%v want integrity_check.already_running; body=%v",
			body["code"], body)
	}
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing: %v", body["details"])
	}
	jobStartedAt, _ := details["job_started_at"].(string)
	if jobStartedAt == "" {
		t.Fatalf("details.job_started_at empty on 409")
	}
	parsed, err := time.Parse(time.RFC3339, jobStartedAt)
	if err != nil {
		t.Fatalf("details.job_started_at %q not RFC3339: %v", jobStartedAt, err)
	}
	// Within 2 s of seeded value (RFC3339 truncates sub-second precision).
	if diff := parsed.Sub(startedAt.UTC().Truncate(time.Second)); diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("details.job_started_at=%v vs seeded=%v (diff=%v)",
			parsed, startedAt, diff)
	}
}

// TestAdminDBHealthCheck_RequiresSuperAdmin — non-super-admin caller must
// receive 403 with a permission-class envelope. The handler never runs;
// lease state is untouched.
func TestAdminDBHealthCheck_RequiresSuperAdmin(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	// Non-super-admin user.
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	code, _, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusForbidden {
		t.Fatalf("code=%d want 403; body=%v", code, body)
	}
	if body["class"] != "permission" {
		t.Fatalf("class=%v want permission", body["class"])
	}
	// Lease must not have been acquired.
	if state := api.DBHealthJobStateForTest(); state == "running" {
		t.Fatalf("lease was acquired on a 403 path; state=%q", state)
	}
}

// TestAdminDBHealthCheck_EmitsTriggeredAudit — a successful 202 produces
// an admin.integrity_check.triggered audit row attributed to the caller.
// The goroutine may or may not have completed yet; the assertion is
// about the synchronous emit at POST entry.
func TestAdminDBHealthCheck_EmitsTriggeredAudit(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	before := countAuditRows(t, s, audit.EvtIntegrityCheckTriggered)

	code, _, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusAccepted {
		t.Fatalf("code=%d want 202; body=%v", code, body)
	}

	after := countAuditRows(t, s, audit.EvtIntegrityCheckTriggered)
	if after-before != 1 {
		t.Fatalf("admin.integrity_check.triggered rows: before=%d after=%d delta=%d want 1",
			before, after, after-before)
	}

	if state := waitForLeaseIdle(t, 5*time.Second); state != "idle" {
		t.Fatalf("lease stuck at state=%q after 5s", state)
	}
}

// TestAdminDBHealthCheck_GoroutineCompletesAndEmitsCompleted — the
// goroutine must emit admin.integrity_check.completed (on a healthy DB)
// and write db.integrity_check.last_manual_at. Polling waits up to 5 s
// for the lease to flip back to "idle".
func TestAdminDBHealthCheck_GoroutineCompletesAndEmitsCompleted(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	beforeCompleted := countAuditRows(t, s, audit.EvtIntegrityCheckCompleted)

	code, _, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusAccepted {
		t.Fatalf("code=%d want 202; body=%v", code, body)
	}

	if state := waitForLeaseIdle(t, 5*time.Second); state != "idle" {
		t.Fatalf("goroutine never finished; lease state=%q", state)
	}

	afterCompleted := countAuditRows(t, s, audit.EvtIntegrityCheckCompleted)
	if afterCompleted-beforeCompleted != 1 {
		t.Fatalf("admin.integrity_check.completed rows: before=%d after=%d delta=%d want 1",
			beforeCompleted, afterCompleted, afterCompleted-beforeCompleted)
	}

	// The goroutine must have written last_manual_at.
	ctx := context.Background()
	v, err := s.deps.Settings.Get(ctx, metadata.SettingDBIntegrityCheckLastManualAt)
	if err != nil {
		t.Fatalf("settings.Get(last_manual_at) after goroutine: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		t.Fatalf("last_manual_at %q not RFC3339: %v", v, err)
	}
}

// TestAdminDBHealthCheck_LeaseReleasedOnPanic — inject a runner that
// panics on every call. The goroutine's defer+recover MUST restore
// state="idle" with lastStatus="panicked" so subsequent POSTs do not
// return 409. Without this invariant, Pitfall 10.4 regresses silently.
func TestAdminDBHealthCheck_LeaseReleasedOnPanic(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Inject a runner that panics. The handler emits
	// admin.integrity_check.triggered BEFORE launching the goroutine, so
	// we still expect that event on the audit table.
	api.SetIntegrityCheckRunnerForTest(t, func(
		_ context.Context, _ *metadata.DB, _ *metadata.SettingsRepo,
		_ metadata.AuditRecorder, _ string,
	) (string, int64) {
		panic("synthetic panic for lease-unwind test")
	})

	code, _, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusAccepted {
		t.Fatalf("code=%d want 202; body=%v", code, body)
	}

	// Wait for the goroutine's defer to unwind the lease.
	if state := waitForLeaseIdle(t, 5*time.Second); state != "idle" {
		t.Fatalf("lease not released after panic; state=%q", state)
	}
	if last := api.DBHealthJobLastStatusForTest(); last != "panicked" {
		t.Fatalf("lastStatus=%q after panic; want panicked", last)
	}

	// Subsequent POST must NOT return 409 — the lease was released. The
	// rate-limit window is now active though (lastRunAt=now), so we
	// expect 429, NOT 409. 429 is the positive signal here: it proves
	// (a) the lease was released (not still running) and (b) lastRunAt
	// was stamped by the panic-unwind path.
	code2, retryAfter2, body2 := doPostHealthCheck(t, s, cookie)
	if code2 == http.StatusConflict {
		t.Fatalf("second POST returned 409 — lease NOT released on panic; body=%v", body2)
	}
	if code2 != http.StatusTooManyRequests {
		t.Fatalf("second POST code=%d want 429; body=%v", code2, body2)
	}
	if retryAfter2 == "" {
		t.Fatalf("Retry-After missing on post-panic 429")
	}
}

// TestAdminDBHealthCheck_SequentialCallAfterWindow — lastRunAt older than
// the rate-limit window permits a fresh 202. Seeds lastRunAt to 2h ago.
func TestAdminDBHealthCheck_SequentialCallAfterWindow(t *testing.T) {
	s := newTestServer(t)
	api.ResetDBHealthJobForTest(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	api.SetDBHealthJobLastRunAtForTest(t, time.Now().Add(-2*time.Hour))

	code, _, body := doPostHealthCheck(t, s, cookie)
	if code != http.StatusAccepted {
		t.Fatalf("code=%d want 202 (lastRunAt=2h ago is outside window); body=%v",
			code, body)
	}

	if state := waitForLeaseIdle(t, 5*time.Second); state != "idle" {
		t.Fatalf("lease stuck at state=%q after 5s", state)
	}
}
