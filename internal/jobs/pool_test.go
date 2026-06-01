package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/jobs"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// testCfg returns Jobs config with short poll so tests don't sit idle.
func testCfg(workers int) config.Jobs {
	return config.Jobs{
		SyncWorkers:          workers,
		ScanWorkers:          workers,
		PollInterval:         50 * time.Millisecond,
		ShutdownGraceSeconds: 1,
	}
}

// enqueueN inserts n pending sync_jobs rows using the real repo.
func enqueueN(t *testing.T, db *metadata.DB, repo *metadata.SyncJobsRepo, kind string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
			_, err := repo.Enqueue(context.Background(), tx, kind, 0, 0, fmt.Sprintf(`{"i":%d}`, i))
			return err
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
}

// TestPool_TenJobsTwoWorkers runs 10 jobs through a 2-worker sync pool
// and asserts: all 10 processed exactly once, no double-execution. This
// is the plan's "10-job/2-worker" acceptance criterion.
func TestPool_TenJobsTwoWorkers(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	var processed int64
	seen := &sync.Map{} // id -> struct{}
	handler := func(ctx context.Context, j *jobs.JobView) error {
		if _, loaded := seen.LoadOrStore(j.ID, struct{}{}); loaded {
			t.Errorf("double-execution for id=%d", j.ID)
		}
		atomic.AddInt64(&processed, 1)
		return nil
	}

	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"k": handler}, testCfg(2))

	enqueueN(t, db, repo, "k", 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&processed) >= 10 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&processed); got != 10 {
		t.Fatalf("processed=%d want 10", got)
	}

	p.Shutdown(ctx, 500*time.Millisecond)
}

// TestPool_FailingHandlerRetriesThenPermanentFail asserts that after
// MaxAttempts failures the row is marked 'failed' permanently.
func TestPool_FailingHandlerRetriesThenPermanentFail(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	var calls int64
	handler := func(ctx context.Context, j *jobs.JobView) error {
		atomic.AddInt64(&calls, 1)
		return errors.New("boom")
	}

	cfg := testCfg(1)
	cfg.PollInterval = 20 * time.Millisecond
	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"k": handler}, cfg)

	// Seed one job and force next_run_at to a very old value so retry
	// schedule (1m minimum) doesn't block the test.
	var id int64
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = repo.Enqueue(context.Background(), tx, "k", 0, 0, "{}")
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Drive the retry loop by resetting next_run_at to now after each
	// observed increment. Expect MaxAttempts handler calls total before
	// the row goes terminal.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var attempts int64
		_ = db.Reader.QueryRow(`SELECT status, attempts FROM sync_jobs WHERE id=?`, id).Scan(&status, &attempts)
		if status == "failed" {
			break
		}
		// Push next_run_at back so the dispatcher picks it up again.
		_, _ = db.Writer.Exec(`UPDATE sync_jobs SET next_run_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`, id)
		time.Sleep(30 * time.Millisecond)
	}

	p.Shutdown(ctx, 500*time.Millisecond)

	var status string
	var attempts int64
	if err := db.Reader.QueryRow(`SELECT status, attempts FROM sync_jobs WHERE id=?`, id).Scan(&status, &attempts); err != nil {
		t.Fatalf("post-query: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
	if got := atomic.LoadInt64(&calls); got < int64(jobs.MaxAttempts) {
		t.Fatalf("handler calls=%d want >= %d", got, jobs.MaxAttempts)
	}
}

// TestPool_NoHandlerMarksFailed asserts that a job kind with no
// registered handler gets failed (so the queue isn't blocked by poison
// rows) rather than looping forever.
func TestPool_NoHandlerMarksFailed(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	cfg := testCfg(1)
	cfg.PollInterval = 20 * time.Millisecond
	p := jobs.NewSyncPool(db, repo, jobs.Handlers{}, cfg)

	var id int64
	_ = db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = repo.Enqueue(context.Background(), tx, "unknown", 0, 0, "{}")
		return err
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// The row is initially {status='pending', attempts=0}. The worker
	// leases it (status='running'), markFailed flips it back to
	// 'pending' and increments attempts in a single UPDATE. Polling on
	// status alone races the initial state — the test was reading the
	// pre-lease snapshot and asserting attempts>=1, which intermittently
	// failed. Wait until attempts have actually advanced (or we see the
	// terminal 'failed' status after MaxAttempts retries) before
	// asserting.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var attempts int64
		_ = db.Reader.QueryRow(`SELECT status, attempts FROM sync_jobs WHERE id=?`, id).Scan(&status, &attempts)
		if attempts >= 1 && (status == "pending" || status == "failed") {
			p.Shutdown(ctx, 500*time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.Shutdown(ctx, 500*time.Millisecond)
	t.Fatal("timed out waiting for no-handler row to be marked failed")
}

// TestPool_KickTriggersImmediateDispatch asserts that Kick() cuts the
// dispatch latency below the 2s default poll interval. We use a 1s poll
// here so a "kick fires within 100ms" observation is meaningful.
func TestPool_KickTriggersImmediateDispatch(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	done := make(chan time.Time, 1)
	handler := func(ctx context.Context, j *jobs.JobView) error {
		done <- time.Now()
		return nil
	}

	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"k": handler}, config.Jobs{
		SyncWorkers:          1,
		ScanWorkers:          1,
		PollInterval:         1 * time.Second,
		ShutdownGraceSeconds: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Wait for the first poll to complete (queue empty) to avoid the
	// "poll once immediately" head-start confusing the latency measure.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	_ = db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := repo.Enqueue(context.Background(), tx, "k", 0, 0, "{}")
		return err
	})
	p.Kick()

	select {
	case t0 := <-done:
		lat := t0.Sub(start)
		if lat > 500*time.Millisecond {
			t.Fatalf("kick latency=%v exceeds 500ms (poll=1s; kick should short-circuit)", lat)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("kick dispatch timed out")
	}

	p.Shutdown(ctx, 500*time.Millisecond)
}

// TestPool_ShutdownDeadlineAbandonsLongHandler asserts that a handler
// which outlasts the grace deadline is abandoned and the row is left
// 'running' (boot recovery re-pends it next start).
func TestPool_ShutdownDeadlineAbandonsLongHandler(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	started := make(chan struct{})
	handler := func(ctx context.Context, j *jobs.JobView) error {
		close(started)
		// Respect cancel quickly so the worker goroutine actually exits
		// after Shutdown's grace — but longer than the grace window so
		// the row shouldn't reach MarkDone.
		select {
		case <-ctx.Done():
			// Deliberate: sleep longer than grace to hold 'running'.
			time.Sleep(300 * time.Millisecond)
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	}

	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"k": handler}, testCfg(1))

	enqueueN(t, db, repo, "k", 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	<-started // handler got the row

	t0 := time.Now()
	p.Shutdown(ctx, 100*time.Millisecond)
	elapsed := time.Since(t0)
	if elapsed > 300*time.Millisecond {
		t.Fatalf("Shutdown took %v; grace=100ms should bound return", elapsed)
	}

	// Row should still be 'running' — boot recovery is responsible.
	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs`).Scan(&status)
	if status != "running" {
		t.Fatalf("status=%q want running after abandoned shutdown", status)
	}
}

// TestPool_ShutdownFastHandlerExitsCleanly verifies Shutdown returns
// promptly when handlers are quick, without hitting the grace deadline.
func TestPool_ShutdownFastHandlerExitsCleanly(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	handler := func(ctx context.Context, j *jobs.JobView) error {
		return nil
	}
	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"k": handler}, testCfg(1))
	enqueueN(t, db, repo, "k", 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	time.Sleep(150 * time.Millisecond) // let it process

	t0 := time.Now()
	p.Shutdown(ctx, 500*time.Millisecond)
	if elapsed := time.Since(t0); elapsed > 300*time.Millisecond {
		t.Fatalf("Shutdown took %v for fast handler; should be near-instant", elapsed)
	}
}

// TestPool_PanicHandlerDoesNotWedgePool asserts CR-02: a handler panic is
// recovered inside handle(), converted into a job-level failure, and the
// worker goroutine survives to process subsequent jobs. Before the fix the
// panic would unwind the worker; after workerCount panics the dispatcher
// would block forever trying to send into p.workers.
func TestPool_PanicHandlerDoesNotWedgePool(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSyncJobsRepo(db)

	var processed int64
	handler := func(ctx context.Context, j *jobs.JobView) error {
		atomic.AddInt64(&processed, 1)
		if atomic.LoadInt64(&processed) <= 2 {
			// Two panics in a two-worker pool would historically wedge every
			// worker; the recover() guard turns them into MarkFailed rows.
			panic("boom")
		}
		return nil
	}

	cfg := testCfg(2)
	cfg.PollInterval = 20 * time.Millisecond
	p := jobs.NewSyncPool(db, repo, jobs.Handlers{"k": handler}, cfg)

	// Seed 4 jobs: 2 will panic, the remaining 2 must still be processed by
	// the surviving workers. If the recover() is removed, this test hangs
	// until the deadline and fails.
	enqueueN(t, db, repo, "k", 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// Kick old panic rows back to runnable so they get re-leased and
		// eventually succeed, proving end-to-end pool health.
		_, _ = db.Writer.Exec(`UPDATE sync_jobs SET next_run_at=CURRENT_TIMESTAMP WHERE status='pending'`)
		if atomic.LoadInt64(&processed) >= 4 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&processed); got < 4 {
		t.Fatalf("processed=%d want >= 4 (pool is wedged — recover() missing?)", got)
	}

	p.Shutdown(ctx, 500*time.Millisecond)
}

// TestPool_ScanPoolAdapter proves NewScanPool drives the scans repo.
func TestPool_ScanPoolAdapter(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	scansRepo := metadata.NewScansRepo(db)

	var processed int64
	handler := func(ctx context.Context, j *jobs.JobView) error {
		atomic.AddInt64(&processed, 1)
		return nil
	}
	p := jobs.NewScanPool(db, scansRepo, jobs.Handlers{"docker": handler}, testCfg(1))

	// Seed a scans row. scans need a repo_id but FK isn't enforced in
	// modernc/sqlite unless PRAGMA foreign_keys=ON; sqlitetest sets it
	// per Phase 1 pragmas.go — use a dummy repo row.
	// Insert minimal project+repo to satisfy FK.
	ctx := context.Background()
	var projectID, repoID int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO projects(name) VALUES('p')`)
		if err != nil {
			return err
		}
		projectID, _ = res.LastInsertId()
		res, err = tx.ExecContext(ctx, `INSERT INTO repos(project_id, type, name) VALUES(?,?,?)`, projectID, "docker", "r")
		if err != nil {
			return err
		}
		repoID, _ = res.LastInsertId()
		_, err = scansRepo.Enqueue(ctx, tx, repoID, "docker", "sha256:abc")
		return err
	})

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(runCtx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&processed) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&processed); got != 1 {
		t.Fatalf("scan-pool processed=%d want 1", got)
	}
	p.Shutdown(runCtx, 500*time.Millisecond)
}
