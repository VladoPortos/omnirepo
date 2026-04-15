package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// Handler processes one leased job. Returning nil marks the job done;
// a non-nil error schedules a retry (or terminal failure after
// MaxAttempts). Handler MUST honor ctx cancellation — the Pool cancels
// ctx during shutdown drain.
type Handler func(ctx context.Context, job *JobView) error

// HandlerFunc is a convenience alias so downstream plans can register
// plain functions as handlers.
type HandlerFunc = Handler

// Handlers maps job kind ("pull_external", "promote", "gc", "scan_image"
// etc.) to its handler. Empty maps are valid; the Pool will lease rows
// and immediately fail them with "no handler for kind" so poison rows
// don't block the queue. Downstream plans (02-09 / 02-10 / 02-12) own
// the population of this map at app.Run wire time.
type Handlers map[string]Handler

// Pool is a generic DB-leased job runner. One Pool backs sync_jobs, a
// second Pool backs scans. They differ only in LeaseRepo adapter, worker
// count, and logged name.
type Pool struct {
	name        string
	db          *metadata.DB
	repo        LeaseRepo
	handlers    Handlers
	workerCount int
	poll        time.Duration

	kick    chan struct{}
	workers chan *JobView

	// ctx/cancel are created on Run; Shutdown cancels ctx to stop the
	// dispatcher + all running handlers. initMu guards runCtx/cancel so
	// concurrent Run + Shutdown are race-free even when Shutdown is
	// called before Run has a chance to set them.
	initMu     sync.Mutex
	runCtx     context.Context
	cancel     context.CancelFunc
	started    bool
	stopped    bool
	dispatchWG sync.WaitGroup
	workerWG   sync.WaitGroup
}

// NewSyncPool constructs the sync-job pool. cfg.SyncWorkers and
// cfg.PollInterval tune the goroutine count + dispatcher cadence.
func NewSyncPool(db *metadata.DB, repo *metadata.SyncJobsRepo, handlers Handlers, cfg config.Jobs) *Pool {
	return newPool("sync", db, SyncJobsAdapter(repo), handlers, cfg.SyncWorkers, cfg.PollInterval)
}

// NewScanPool constructs the scan-pool. Adapter flattens Scan rows into
// the same JobView type the dispatcher uses.
func NewScanPool(db *metadata.DB, repo *metadata.ScansRepo, handlers Handlers, cfg config.Jobs) *Pool {
	return newPool("scan", db, ScansAdapter(repo), handlers, cfg.ScanWorkers, cfg.PollInterval)
}

// newPool is the shared constructor. Used by the sync/scan entry points
// and by tests that inject a fake LeaseRepo.
func newPool(name string, db *metadata.DB, repo LeaseRepo, handlers Handlers, workers int, poll time.Duration) *Pool {
	if workers < 1 {
		workers = 1
	}
	if poll <= 0 {
		poll = 2 * time.Second
	}
	return &Pool{
		name:        name,
		db:          db,
		repo:        repo,
		handlers:    handlers,
		workerCount: workers,
		poll:        poll,
		kick:        make(chan struct{}, 1),
		workers:     make(chan *JobView, workers),
	}
}

// Kick hints the dispatcher that a new job may be ready without waiting
// for the poll tick. Non-blocking: if the buffered-1 channel is already
// full the signal is dropped (the pending tick will handle it). D-16.
func (p *Pool) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is canceled (or Shutdown is called). It spawns
// workerCount worker goroutines and one dispatcher goroutine.
//
// Calling Run more than once is a no-op on the second call.
func (p *Pool) Run(parent context.Context) {
	p.initMu.Lock()
	firstRun := !p.started
	if firstRun {
		p.started = true
		// Respect an early Shutdown: if stopped was already called, don't
		// start anything.
		if p.stopped {
			p.initMu.Unlock()
			return
		}
		p.runCtx, p.cancel = context.WithCancel(parent)
		// Spawn workers.
		for i := 0; i < p.workerCount; i++ {
			p.workerWG.Add(1)
			go p.worker(p.runCtx)
		}
		// Spawn dispatcher.
		p.dispatchWG.Add(1)
		go p.dispatch(p.runCtx)
	}
	p.initMu.Unlock()
	// Block until dispatcher exits (i.e. ctx canceled).
	p.dispatchWG.Wait()
}

// dispatch polls + reacts to kicks and feeds workers.
func (p *Pool) dispatch(ctx context.Context) {
	defer p.dispatchWG.Done()
	tick := time.NewTicker(p.poll)
	defer tick.Stop()

	drainPending := func() {
		for {
			if ctx.Err() != nil {
				return
			}
			j, ok, err := p.repo.LeaseOne(ctx, p.name+":"+randToken(8))
			if err != nil {
				// Context cancel during lease is expected on shutdown.
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					slog.Error("jobs.lease.err", "pool", p.name, "err", err)
				}
				return
			}
			if !ok {
				return
			}
			select {
			case p.workers <- j:
			case <-ctx.Done():
				return
			}
		}
	}

	// Poll once immediately so enqueue-before-Run races land quickly.
	drainPending()
	for {
		select {
		case <-ctx.Done():
			close(p.workers)
			return
		case <-tick.C:
			drainPending()
		case <-p.kick:
			drainPending()
		}
	}
}

// worker consumes leased jobs until the channel is closed by dispatch.
func (p *Pool) worker(ctx context.Context) {
	defer p.workerWG.Done()
	for j := range p.workers {
		p.handle(ctx, j)
	}
}

// handle dispatches one job to its handler and records the outcome.
func (p *Pool) handle(ctx context.Context, j *JobView) {
	h, ok := p.handlers[j.Kind]
	if !ok {
		p.markFailed(ctx, j, fmt.Errorf("no handler for kind %q", j.Kind))
		return
	}
	err := h(ctx, j)
	if err == nil {
		if derr := p.db.WriteTx(ctx, func(tx *sql.Tx) error {
			return p.repo.MarkDone(ctx, tx, j.ID)
		}); derr != nil {
			// MarkDone failing is rare; log and move on — the row stays
			// 'running' and boot recovery (D-19) will re-pend it. Don't
			// MarkFailed: the handler already succeeded.
			slog.Error("jobs.markdone.err", "pool", p.name, "id", j.ID, "err", derr)
		}
		return
	}
	p.markFailed(ctx, j, err)
}

// markFailed decides between transient retry (MarkFailed) and permanent
// termination (MarkPermanentlyFailed) based on attempts+1 vs MaxAttempts.
func (p *Pool) markFailed(ctx context.Context, j *JobView, herr error) {
	nextAttempts := int(j.Attempts) + 1
	// If ctx is already canceled (shutdown drain), don't try to update
	// the row — boot recovery will handle it next boot. Using a fresh
	// short-deadline context would still race against DB close. Simpler
	// and provably safe: bail.
	if ctx.Err() != nil {
		slog.Warn("jobs.markfailed.skipped.shutdown", "pool", p.name, "id", j.ID)
		return
	}
	if nextAttempts >= MaxAttempts {
		if derr := p.db.WriteTx(ctx, func(tx *sql.Tx) error {
			return p.repo.MarkPermanentlyFailed(ctx, tx, j.ID, herr.Error())
		}); derr != nil {
			slog.Error("jobs.markpermfailed.err", "pool", p.name, "id", j.ID, "err", derr)
		}
		return
	}
	nextRunAt := time.Now().Add(Backoff(nextAttempts))
	if derr := p.db.WriteTx(ctx, func(tx *sql.Tx) error {
		return p.repo.MarkFailed(ctx, tx, j.ID, herr.Error(), nextRunAt)
	}); derr != nil {
		slog.Error("jobs.markfailed.err", "pool", p.name, "id", j.ID, "err", derr)
	}
}

// Shutdown cancels the dispatcher and waits up to grace for in-flight
// handlers to finish. If the deadline hits first, outstanding handlers
// are abandoned (their rows stay 'running' and boot recovery re-pends
// them, D-19+D-20). Safe to call more than once.
func (p *Pool) Shutdown(parent context.Context, grace time.Duration) {
	p.initMu.Lock()
	alreadyStopped := p.stopped
	p.stopped = true
	cancel := p.cancel
	p.initMu.Unlock()
	if !alreadyStopped && cancel != nil {
		cancel()
	}
	// Wait for dispatcher AND workers, bounded by grace.
	done := make(chan struct{})
	go func() {
		p.dispatchWG.Wait()
		p.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		slog.Warn("jobs.shutdown.deadline", "pool", p.name, "grace", grace.String())
	case <-parent.Done():
		slog.Warn("jobs.shutdown.parent_canceled", "pool", p.name)
	}
}

// randToken returns a hex-encoded random string for lease ownership.
// 8 bytes = 16 hex chars = plenty for uniqueness within one boot.
func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
