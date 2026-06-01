// White-box tests for the admin DB-health lease singleton. Lives in
// package api so it can touch the package-private dbHealthJob without
// exporting a test-only hook.
//
// Black-box HTTP tests are in admin_db_health_test.go (package api_test)
// and admin_db_health_check_test.go (package api_test).
package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// ResetDBHealthJobForTest restores dbHealthJob to its zero/idle state
// and registers a t.Cleanup that restores it again at test end, so
// parallel or sequential tests do not leak lease state.
//
// Exported at lowercase-name-in-test-files via package-scope so both
// internal and external (via type alias trick) test files can call it.
// The _test.go suffix confines the export to test builds.
func ResetDBHealthJobForTest(t *testing.T) {
	t.Helper()
	// Capture field values (NOT the struct, which would copy the mutex
	// and trip `go vet`'s copylocks check). Restore field-by-field on
	// cleanup so tests don't bleed lease state across each other.
	dbHealthJob.mu.Lock()
	prevState := dbHealthJob.state
	prevStartedAt := dbHealthJob.startedAt
	prevLastRunAt := dbHealthJob.lastRunAt
	prevLastStatus := dbHealthJob.lastStatus
	dbHealthJob.state = "idle"
	dbHealthJob.startedAt = time.Time{}
	dbHealthJob.lastRunAt = time.Time{}
	dbHealthJob.lastStatus = ""
	dbHealthJob.mu.Unlock()
	t.Cleanup(func() {
		dbHealthJob.mu.Lock()
		dbHealthJob.state = prevState
		dbHealthJob.startedAt = prevStartedAt
		dbHealthJob.lastRunAt = prevLastRunAt
		dbHealthJob.lastStatus = prevLastStatus
		dbHealthJob.mu.Unlock()
	})
}

// TestAdminDBHealth_LeaseRateLimitWindow covers the can_run_now /
// next_available_at server-driven eligibility signal.
//
// With lastRunAt 30 minutes ago, the lease must report can_run_now=false
// and next_available_at ~ 30 minutes from now (bounded by the
// integrityRateLimitWindow constant).
func TestAdminDBHealth_LeaseRateLimitWindow(t *testing.T) {
	ResetDBHealthJobForTest(t)

	// Simulate: manual check completed 30 min ago.
	halfWindowAgo := time.Now().Add(-integrityRateLimitWindow / 2)
	dbHealthJob.mu.Lock()
	dbHealthJob.lastRunAt = halfWindowAgo
	dbHealthJob.lastStatus = "ok"
	dbHealthJob.mu.Unlock()

	snap := dbHealthJob.Snapshot()
	if snap.Running {
		t.Fatalf("Running=true after completed run; want false")
	}
	if snap.CanRunNow {
		t.Fatalf("CanRunNow=true within the rate-limit window; want false")
	}
	if snap.NextAvailableAt == "" {
		t.Fatalf("NextAvailableAt empty when CanRunNow=false")
	}
	nextAt, err := time.Parse(time.RFC3339, snap.NextAvailableAt)
	if err != nil {
		t.Fatalf("NextAvailableAt not RFC3339: %q (%v)", snap.NextAvailableAt, err)
	}
	// Expected: halfWindowAgo + 1h = now + ~30 min. Allow 1-minute
	// wall-clock slack so slow test runners don't flake.
	wantAt := halfWindowAgo.Add(integrityRateLimitWindow)
	if diff := nextAt.Sub(wantAt); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("NextAvailableAt=%v, want ~%v (diff=%v)", nextAt, wantAt, diff)
	}
	if snap.LastManualRunAt == "" {
		t.Fatalf("LastManualRunAt empty despite completed run")
	}
}

// TestAdminDBHealth_LeaseNeverRun asserts the never-run lease reports
// CanRunNow=true and no NextAvailableAt (empty string on the wire).
func TestAdminDBHealth_LeaseNeverRun(t *testing.T) {
	ResetDBHealthJobForTest(t)

	snap := dbHealthJob.Snapshot()
	if snap.Running {
		t.Fatalf("Running=true on never-run lease")
	}
	if !snap.CanRunNow {
		t.Fatalf("CanRunNow=false on never-run lease; want true")
	}
	if snap.NextAvailableAt != "" {
		t.Fatalf("NextAvailableAt=%q on never-run lease; want empty", snap.NextAvailableAt)
	}
	if snap.LastManualRunAt != "" {
		t.Fatalf("LastManualRunAt=%q on never-run lease; want empty", snap.LastManualRunAt)
	}
}

// TestAdminDBHealth_LeaseRunningReportsStartedAt asserts a lease that is
// mid-run reports Running=true with a non-empty JobStartedAt. The POST
// handler transitions state to "running"; this test pins the snapshot
// contract that handler depends on.
func TestAdminDBHealth_LeaseRunningReportsStartedAt(t *testing.T) {
	ResetDBHealthJobForTest(t)

	now := time.Now()
	dbHealthJob.mu.Lock()
	dbHealthJob.state = "running"
	dbHealthJob.startedAt = now
	dbHealthJob.mu.Unlock()

	snap := dbHealthJob.Snapshot()
	if !snap.Running {
		t.Fatalf("Running=false; want true while state=running")
	}
	if snap.JobStartedAt == "" {
		t.Fatalf("JobStartedAt empty while state=running")
	}
	parsed, err := time.Parse(time.RFC3339, snap.JobStartedAt)
	if err != nil {
		t.Fatalf("JobStartedAt not RFC3339: %q (%v)", snap.JobStartedAt, err)
	}
	if diff := parsed.Sub(now.UTC().Truncate(time.Second)); diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("JobStartedAt=%v vs now=%v (diff=%v)", parsed, now, diff)
	}
}

// TestAdminDBHealth_LeaseSnapshotConcurrency is a light race-detector
// coverage gate: concurrent calls to Snapshot() from multiple goroutines
// must not trip -race. The rate-limit test + the Running test touch the
// fields under mu; this test reads under mu in parallel.
func TestAdminDBHealth_LeaseSnapshotConcurrency(t *testing.T) {
	ResetDBHealthJobForTest(t)
	dbHealthJob.mu.Lock()
	dbHealthJob.lastRunAt = time.Now().Add(-10 * time.Minute)
	dbHealthJob.mu.Unlock()

	const goroutines = 16
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = dbHealthJob.Snapshot()
			}
		}()
	}
	wg.Wait()
}

// -----------------------------------------------------------------------------
// Test hooks used by admin_db_health_check_test.go (package
// api_test). These live in an _test.go file so the symbols do not ship in
// production binaries.
// -----------------------------------------------------------------------------

// SetDBHealthJobLastRunAtForTest overwrites dbHealthJob.lastRunAt so
// tests can simulate "a manual run completed N ago" without driving the
// goroutine. The previous value is restored on t.Cleanup.
//
// Callers should have already invoked ResetDBHealthJobForTest(t) in the
// same test body — otherwise state leaks from whatever test ran before.
func SetDBHealthJobLastRunAtForTest(t *testing.T, when time.Time) {
	t.Helper()
	dbHealthJob.mu.Lock()
	prev := dbHealthJob.lastRunAt
	dbHealthJob.lastRunAt = when
	dbHealthJob.mu.Unlock()
	t.Cleanup(func() {
		dbHealthJob.mu.Lock()
		dbHealthJob.lastRunAt = prev
		dbHealthJob.mu.Unlock()
	})
}

// SetDBHealthJobRunningForTest forces the lease into state="running" with
// startedAt=when. Used by the 409 already_running test. Previous fields
// are captured & restored on t.Cleanup. Callers should still invoke
// ResetDBHealthJobForTest(t) first to isolate from other tests.
func SetDBHealthJobRunningForTest(t *testing.T, when time.Time) {
	t.Helper()
	dbHealthJob.mu.Lock()
	prevState := dbHealthJob.state
	prevStartedAt := dbHealthJob.startedAt
	dbHealthJob.state = "running"
	dbHealthJob.startedAt = when
	dbHealthJob.mu.Unlock()
	t.Cleanup(func() {
		dbHealthJob.mu.Lock()
		dbHealthJob.state = prevState
		dbHealthJob.startedAt = prevStartedAt
		dbHealthJob.mu.Unlock()
	})
}

// DBHealthJobStateForTest returns the current dbHealthJob.state under
// lock. Used by the goroutine-polling tests to wait for
// the goroutine to flip state back to "idle".
func DBHealthJobStateForTest() string {
	dbHealthJob.mu.Lock()
	defer dbHealthJob.mu.Unlock()
	return dbHealthJob.state
}

// DBHealthJobLastStatusForTest returns dbHealthJob.lastStatus under lock.
// The panic-recovery test asserts this is "panicked" after the goroutine
// unwinds on a forced runner panic.
func DBHealthJobLastStatusForTest() string {
	dbHealthJob.mu.Lock()
	defer dbHealthJob.mu.Unlock()
	return dbHealthJob.lastStatus
}

// SetIntegrityCheckRunnerForTest swaps the package-level
// integrityCheckRunner with the supplied function and registers a
// t.Cleanup that restores the original (metadata.RunIntegrityCheckNow).
//
// Used by the panic-recovery test to inject a runner that panics on
// every call, and by the audit-emission test to run the real runner
// against the harness DB. The type signature here mirrors
// metadata.RunIntegrityCheckNow verbatim so swapping does not widen the
// contract the handler depends on.
func SetIntegrityCheckRunnerForTest(
	t *testing.T,
	runner func(ctx context.Context, db *metadata.DB, settings *metadata.SettingsRepo, auditRec metadata.AuditRecorder, source string) (string, int64),
) {
	t.Helper()
	prev := integrityCheckRunner
	integrityCheckRunner = runner
	t.Cleanup(func() { integrityCheckRunner = prev })
}
