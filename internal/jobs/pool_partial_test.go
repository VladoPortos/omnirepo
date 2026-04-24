package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// stubPartialErr satisfies internal/jobs.PartialSyncError structurally
// without importing the helm package. The jobs-package routing tests
// exercise the interface gate independently of any concrete helm type;
// Plan 05 covers the helm-to-jobs end-to-end path.
type stubPartialErr struct {
	persisted int64
	expected  int64
}

func (s *stubPartialErr) Persisted() int64 { return s.persisted }
func (s *stubPartialErr) Expected() int64  { return s.expected }
func (s *stubPartialErr) Error() string    { return "stub: partial sync error" }

// TestPool_HelmPartialSync_TerminalFailed asserts that a helm_sync
// handler returning a PartialSyncError-satisfying error is routed to the
// terminal 'failed' state in a single handler call with the canonical
// 3-field partial-log JSON written atomically (D-01, D-03a, D-04).
// No retry ladder, no MaxAttempts iteration.
func TestPool_HelmPartialSync_TerminalFailed(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	var calls int64
	handler := func(ctx context.Context, j *jobs.JobView) error {
		atomic.AddInt64(&calls, 1)
		// Codex Q4 follow-up: wrap the partial error so errors.As must
		// traverse the Unwrap chain (not just the outer type). Proves the
		// interface-target discovery works through fmt.Errorf("%w") layers,
		// which is how the real helm handler returns wrapped cancel causes.
		return fmt.Errorf("pool: %w", &stubPartialErr{persisted: 2, expected: 3})
	}

	cfg := testCfg(1)
	cfg.PollInterval = 20 * time.Millisecond
	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"helm_sync": handler}, cfg)

	var id int64
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = repo.Enqueue(context.Background(), tx, "helm_sync", 0, 0, "{}")
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Poll until the row hits terminal 'failed'.
	deadline := time.Now().Add(5 * time.Second)
	var terminal bool
	for time.Now().Before(deadline) {
		var status string
		if err := db.Reader.QueryRow(
			`SELECT status FROM sync_jobs WHERE id=?`, id,
		).Scan(&status); err == nil && status == "failed" {
			terminal = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.Shutdown(ctx, 500*time.Millisecond)

	if !terminal {
		t.Fatalf("row never reached terminal 'failed' within 5s (calls=%d)",
			atomic.LoadInt64(&calls))
	}

	// Exactly one handler call — D-01 bypass of retry ladder.
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("handler calls=%d want 1 (helm partial-sync must NOT retry)", got)
	}

	// Status + last_error + log observed via single SELECT (atomicity
	// proof — Plan 02's D-04 contract).
	var status, lastError, logStr string
	if err := db.Reader.QueryRow(
		`SELECT status, last_error, log FROM sync_jobs WHERE id=?`, id,
	).Scan(&status, &lastError, &logStr); err != nil {
		t.Fatalf("post-query: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
	if lastError == "" {
		t.Fatalf("last_error is empty — expected sanitised stub error string")
	}
	want := `{"partial":true,"files_persisted":2,"files_expected":3}`
	if logStr != want {
		t.Fatalf("log=%q\nwant %q", logStr, want)
	}
}

// TestPool_NonHelmError_RetriesAsBefore asserts that the helm-specific
// terminal-failed branch does NOT fire for a non-helm kind even when
// the error satisfies PartialSyncError (D-02 scope boundary + T-5-03
// threat mitigation). The generic retry ladder still applies.
func TestPool_NonHelmError_RetriesAsBefore(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	var calls int64
	handler := func(ctx context.Context, j *jobs.JobView) error {
		atomic.AddInt64(&calls, 1)
		// Partial-shaped error with WRONG kind — must route through
		// the generic ladder, not the helm terminal branch.
		return &stubPartialErr{persisted: 2, expected: 3}
	}

	cfg := testCfg(1)
	cfg.PollInterval = 20 * time.Millisecond
	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"gc": handler}, cfg)

	var id int64
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = repo.Enqueue(context.Background(), tx, "gc", 0, 0, "{}")
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Drive the retry ladder: after each MarkFailed flip back to
	// status='pending', the scheduler's next_run_at is ~1m in the
	// future — reset it to now so the next attempt fires promptly.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		_ = db.Reader.QueryRow(
			`SELECT status FROM sync_jobs WHERE id=?`, id,
		).Scan(&status)
		if status == "failed" {
			break
		}
		_, _ = db.Writer.Exec(
			`UPDATE sync_jobs SET next_run_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`, id,
		)
		time.Sleep(30 * time.Millisecond)
	}
	p.Shutdown(ctx, 500*time.Millisecond)

	// Handler must have been invoked at least MaxAttempts times — the
	// generic ladder still ran despite the partial-shaped error
	// (proving kind filter protected the non-helm row).
	if got := atomic.LoadInt64(&calls); got < int64(jobs.MaxAttempts) {
		t.Fatalf("handler calls=%d want >= %d (generic ladder for non-helm kind)",
			got, jobs.MaxAttempts)
	}

	// Status + log assertion — log MUST NOT carry the partial JSON,
	// because the helm branch never fired.
	var status, logStr string
	if err := db.Reader.QueryRow(
		`SELECT status, log FROM sync_jobs WHERE id=?`, id,
	).Scan(&status, &logStr); err != nil {
		t.Fatalf("post-query: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status=%q want failed (should have hit MaxAttempts)", status)
	}
	partial := `{"partial":true`
	if len(logStr) >= len(partial) && logStr[:len(partial)] == partial {
		t.Fatalf("log=%q unexpectedly carries partial JSON — helm branch fired for non-helm kind", logStr)
	}
}

// TestPool_HelmGenericError_RetriesAsBefore asserts that a helm_sync
// row whose error does NOT satisfy PartialSyncError continues through
// the generic retry ladder — only the PartialSyncError class bypasses
// the ladder (T-5-01 error-type confusion mitigation).
func TestPool_HelmGenericError_RetriesAsBefore(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	var calls int64
	handler := func(ctx context.Context, j *jobs.JobView) error {
		atomic.AddInt64(&calls, 1)
		// Plain error — does NOT implement Persisted()/Expected().
		return errors.New("generic boom")
	}

	cfg := testCfg(1)
	cfg.PollInterval = 20 * time.Millisecond
	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"helm_sync": handler}, cfg)

	var id int64
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = repo.Enqueue(context.Background(), tx, "helm_sync", 0, 0, "{}")
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		_ = db.Reader.QueryRow(
			`SELECT status FROM sync_jobs WHERE id=?`, id,
		).Scan(&status)
		if status == "failed" {
			break
		}
		_, _ = db.Writer.Exec(
			`UPDATE sync_jobs SET next_run_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`, id,
		)
		time.Sleep(30 * time.Millisecond)
	}
	p.Shutdown(ctx, 500*time.Millisecond)

	if got := atomic.LoadInt64(&calls); got < int64(jobs.MaxAttempts) {
		t.Fatalf("handler calls=%d want >= %d (generic helm error must retry)",
			got, jobs.MaxAttempts)
	}

	var status, logStr string
	if err := db.Reader.QueryRow(
		`SELECT status, log FROM sync_jobs WHERE id=?`, id,
	).Scan(&status, &logStr); err != nil {
		t.Fatalf("post-query: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
	partial := `{"partial":true`
	if len(logStr) >= len(partial) && logStr[:len(partial)] == partial {
		t.Fatalf("log=%q unexpectedly carries partial JSON — helm branch fired for non-partial helm error", logStr)
	}
}
