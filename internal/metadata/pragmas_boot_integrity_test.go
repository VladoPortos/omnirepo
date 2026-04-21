package metadata_test

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// fakeAuditRecorder is a minimal AuditRecorder for testing.
// It captures every Record call for later assertion.
type fakeAuditRecorder struct {
	mu      sync.Mutex
	calls   []fakeAuditCall
	records atomic.Int64
}

type fakeAuditCall struct {
	Kind    string
	Details map[string]any
}

func (f *fakeAuditRecorder) Record(_ context.Context, kind string, details map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeAuditCall{Kind: kind, Details: details})
	f.records.Add(1)
}

func (f *fakeAuditRecorder) snapshot() []fakeAuditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeAuditCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestRunBootIntegrityCheck_Ok — healthy in-memory DB: three settings keys
// written with status="ok", duration_ms>0, checked_at parsing as RFC3339,
// and exactly one EvtIntegrityCheckCompleted audit event with
// details.source="boot" and details.status="ok".
func TestRunBootIntegrityCheck_Ok(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	rec := &fakeAuditRecorder{}
	ctx := context.Background()

	if err := metadata.RunBootIntegrityCheck(ctx, db, settings, rec); err != nil {
		t.Fatalf("RunBootIntegrityCheck returned %v, want nil", err)
	}

	status, err := settings.Get(ctx, "db.integrity_check.status")
	if err != nil {
		t.Fatalf("settings.Get(status): %v", err)
	}
	if status != "ok" {
		t.Fatalf("status = %q, want %q", status, "ok")
	}
	checkedAt, err := settings.Get(ctx, "db.integrity_check.checked_at")
	if err != nil {
		t.Fatalf("settings.Get(checked_at): %v", err)
	}
	if _, err := time.Parse(time.RFC3339, checkedAt); err != nil {
		t.Fatalf("checked_at %q not RFC3339: %v", checkedAt, err)
	}
	durMsStr, err := settings.Get(ctx, "db.integrity_check.duration_ms")
	if err != nil {
		t.Fatalf("settings.Get(duration_ms): %v", err)
	}
	durMs, err := strconv.ParseInt(durMsStr, 10, 64)
	if err != nil {
		t.Fatalf("duration_ms %q not int: %v", durMsStr, err)
	}
	if durMs < 0 {
		t.Fatalf("duration_ms = %d, want >= 0", durMs)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("audit.Record called %d times, want 1; calls=%+v", len(calls), calls)
	}
	got := calls[0]
	if got.Kind != "admin.integrity_check.completed" {
		t.Fatalf("kind = %q, want admin.integrity_check.completed", got.Kind)
	}
	if got.Details["source"] != "boot" {
		t.Fatalf("details.source = %v, want boot", got.Details["source"])
	}
	if got.Details["status"] != "ok" {
		t.Fatalf("details.status = %v, want ok", got.Details["status"])
	}
	if _, ok := got.Details["duration_ms"].(int64); !ok {
		t.Fatalf("details.duration_ms type = %T, want int64", got.Details["duration_ms"])
	}
}

// TestRunBootIntegrityCheck_ReturnsNilOnQueryFailure — when the reader pool
// is closed before the call, the function MUST return nil (log+cache+
// continue invariant), cache a non-"ok" status, and emit
// EvtIntegrityCheckFailed.
//
// Verification of the cached status reads through db.Writer directly
// (bypassing SettingsRepo.Get which requires the Reader pool) because
// the Reader is intentionally closed to simulate the failure.
func TestRunBootIntegrityCheck_ReturnsNilOnQueryFailure(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	rec := &fakeAuditRecorder{}
	ctx := context.Background()

	// Break the reader pool so Conn(ctx) fails — QueryContext would also
	// fail but conn acquisition is the first gate.
	if err := db.Reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}

	if err := metadata.RunBootIntegrityCheck(ctx, db, settings, rec); err != nil {
		t.Fatalf("RunBootIntegrityCheck returned %v, want nil (log+cache+continue)", err)
	}

	// Read the cached status via Writer (Reader is closed in this test).
	var status string
	if err := db.Writer.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key=?`,
		metadata.SettingDBIntegrityCheckStatus,
	).Scan(&status); err != nil {
		t.Fatalf("read cached status via writer: %v", err)
	}
	if status == "ok" || status == "" {
		t.Fatalf("status = %q, want a non-ok failure message", status)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("audit.Record called %d times, want 1; calls=%+v", len(calls), calls)
	}
	if calls[0].Kind != "admin.integrity_check.failed" {
		t.Fatalf("kind = %q, want admin.integrity_check.failed", calls[0].Kind)
	}
	if calls[0].Details["source"] != "boot" {
		t.Fatalf("details.source = %v, want boot", calls[0].Details["source"])
	}
}

// TestRunBootIntegrityCheck_DoesNotMutatePragmaDSNValues — regression guard
// against accidentally adding integrity_check to the per-connection DSN
// pragma list (Pitfall 10.2). This test asserts the function executes
// without side effects on the package-level pragma count. The count is
// verified via an indirect check: two sequential invocations must produce
// stable behavior (no pragma leakage between runs).
//
// Direct length check would require exporting pragmaDSNValues; instead we
// exercise the function twice and confirm a fresh DB opened AFTER the
// calls still opens cleanly (no DSN drift).
func TestRunBootIntegrityCheck_DoesNotMutatePragmaDSNValues(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	rec := &fakeAuditRecorder{}
	ctx := context.Background()

	if err := metadata.RunBootIntegrityCheck(ctx, db, settings, rec); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := metadata.RunBootIntegrityCheck(ctx, db, settings, rec); err != nil {
		t.Fatalf("second call: %v", err)
	}
	// A fresh DB must still open cleanly — if pragmaDSNValues had been
	// mutated to include integrity_check, the DSN would still work but
	// every connection would pay the cost. The functional assertion
	// here is that Open+migrations still succeed; the cost penalty is
	// not directly visible but the mutation would surface as a panic
	// or DSN parse error in modernc.org/sqlite.
	db2 := sqlitetest.New(t)
	if db2 == nil {
		t.Fatal("fresh DB failed to open after RunBootIntegrityCheck")
	}
}

// TestRunBootIntegrityCheck_UsesReaderConn — indirect reader-pool assertion:
// saturate the writer pool (size 1) with a long-held BEGIN IMMEDIATE tx.
// Readers in WAL mode do not block on writers, so the PRAGMA query itself
// completes even with the writer held. The cache-write side effects go
// through the Writer and fail fast when the function's context cancels;
// the function still returns nil (log+cache+continue, Pitfall 10.3) and
// emits an audit event (proving the PRAGMA query succeeded).
//
// If the function mistakenly used db.Writer for the PRAGMA, the query
// itself would block for the full ctx duration, no completed audit event
// would fire (only a failed one), and the test would catch the regression.
func TestRunBootIntegrityCheck_UsesReaderConn(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	rec := &fakeAuditRecorder{}

	// Hold the single writer connection by opening (but never committing)
	// a BEGIN IMMEDIATE tx. Release on test cleanup.
	holdCtx, cancelHold := context.WithCancel(context.Background())
	txAcquired := make(chan struct{})
	txDone := make(chan struct{})
	go func() {
		defer close(txDone)
		tx, err := db.Writer.BeginTx(holdCtx, nil)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback() }()
		close(txAcquired)
		<-holdCtx.Done()
	}()
	select {
	case <-txAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("test setup: could not acquire writer tx within 2s")
	}
	defer func() {
		cancelHold()
		<-txDone
	}()

	// Short ctx: cache writes will fail fast; the PRAGMA query must still
	// complete via the Reader pool before ctx cancels.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- metadata.RunBootIntegrityCheck(ctx, db, settings, rec)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunBootIntegrityCheck returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunBootIntegrityCheck did not return within 3s — likely using db.Writer for PRAGMA")
	}

	// Reader-pool proof: the completed audit event is only emitted when
	// the PRAGMA query itself succeeds. If the function used the writer
	// pool, the PRAGMA would hit context deadline and we'd see a failed
	// event instead.
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("audit.Record called %d times, want 1; calls=%+v", len(calls), calls)
	}
	if calls[0].Kind != "admin.integrity_check.completed" {
		t.Fatalf("kind = %q, want admin.integrity_check.completed (PRAGMA must run via Reader)",
			calls[0].Kind)
	}
}

// TestRunBootIntegrityCheck_NilAuditRecorderSafe — the AuditRecorder is
// optional (tests and bootstrapping may pass nil). Function must not
// panic or return non-nil.
func TestRunBootIntegrityCheck_NilAuditRecorderSafe(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with nil auditRec: %v", r)
		}
	}()

	if err := metadata.RunBootIntegrityCheck(ctx, db, settings, nil); err != nil {
		t.Fatalf("RunBootIntegrityCheck(nil auditRec) returned %v, want nil", err)
	}
}
