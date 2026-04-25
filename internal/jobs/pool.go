package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/streamio"
)

// MaxLastErrorLen caps the handler-error string persisted into
// sync_jobs.last_error (exposed via GET /sync-jobs/{id}). Plan 08-06 Codex
// rescue Q2: handler errors can embed upstream response bodies or wrapped
// Go error chains; we sanitize + truncate before persist so clients of
// the public API never see a filesystem path, multi-KB HTTP body, or a
// stack-trace-like error chain. Matches the truncateErr convention in
// internal/protocol/deb/sync_handler.go (1 KiB).
const MaxLastErrorLen = 1024

// authHeaderRegex strips Authorization header bytes that some upstream
// client libraries embed in wrapped errors. Same pattern as
// internal/httpx/SanitizeUpstreamErr — duplicated here to keep the jobs
// package free of a httpx import (httpx imports metadata; jobs does not).
var authHeaderRegex = regexp.MustCompile(`(?i)Authorization:\s*[^\r\n"']*`)

// fsPathRegex matches filesystem paths that could leak via wrapped errors
// (e.g. "open /var/lib/omnirepo/repos/proj/deb/repo/pool/...: no such file").
// Conservative shape: starts with `/var/lib/omnirepo/` or `/tmp/` up to the
// next whitespace / end-of-string. Tighter patterns risk false-positives on
// legitimate upstream URLs that contain path-like fragments.
var fsPathRegex = regexp.MustCompile(`/(?:var/lib/omnirepo|tmp)/[^\s:"']+`)

// sanitizeJobError scrubs + truncates a handler error before it lands in
// sync_jobs.last_error. Applied at the pool.markFailed boundary so all
// four sync protocols (+ pull-external) share the same redaction regardless
// of which handler raised the error.
//
// Redactions: Authorization header bytes → "Authorization: REDACTED";
// absolute /var/lib/omnirepo or /tmp paths → "[path]".
// Truncation: MaxLastErrorLen bytes with a "...[truncated]" suffix.
func sanitizeJobError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	s = authHeaderRegex.ReplaceAllString(s, "Authorization: REDACTED")
	s = fsPathRegex.ReplaceAllString(s, "[path]")
	if len(s) > MaxLastErrorLen {
		const suffix = "...[truncated]"
		if cut := MaxLastErrorLen - len(suffix); cut > 0 {
			s = s[:cut] + suffix
		} else {
			s = s[:MaxLastErrorLen]
		}
	}
	return s
}

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
//
// Handler panics are converted into a terminal-style retry via markFailed
// (CR-02). Without this guard a panicking handler would tear down the worker
// goroutine (via runtime unwinding through workerWG.Done's defer) and after
// workerCount panics the dispatcher's `select { case p.workers <- j: }` would
// block forever because no receiver remains — wedging the pool until
// shutdown. scan_pool runs two workers by default so two panics are enough
// to kill scanning entirely; sync_pool suffers the same class of bug.
func (p *Pool) handle(ctx context.Context, j *JobView) {
	h, ok := p.handlers[j.Kind]
	if !ok {
		p.markFailed(ctx, j, fmt.Errorf("no handler for kind %q", j.Kind))
		return
	}
	var err error
	func() {
		defer func() {
			if rv := recover(); rv != nil {
				slog.Error("jobs.handler.panic",
					"pool", p.name, "id", j.ID, "kind", j.Kind, "panic", rv)
				err = fmt.Errorf("handler panic: %v", rv)
			}
		}()
		err = h(ctx, j)
	}()
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
	// v1.5 Phase 5 HELMRETRY-03 (D-01, D-03a, D-04) — helm partial-sync
	// errors terminate at status='failed' BYPASSING the retry ladder,
	// and the 3-field partial-log JSON is written in the SAME UPDATE as
	// the status flip (D-04 atomicity, Plan 02 MarkPermanentlyFailedWithLog).
	//
	// This branch runs BEFORE the ctx.Err() shutdown bail below
	// (Pitfall 1 Option A): graceful-shutdown ctx-cancel is the most
	// common trigger of a partial sync, so losing the terminal write on
	// cancel would leave the row stuck 'running' until boot recovery.
	// If ctx is already cancelled we swap in context.Background() for
	// the DB write so the metadata writer pool (separate lifecycle) can
	// still commit. Boot recovery (Plan 04) is the safety net if the DB
	// is itself mid-close.
	var pse PartialSyncError
	if j.Kind == HelmSyncKind && errors.As(herr, &pse) {
		logJSON := buildPartialLogJSON(pse.Persisted(), pse.Expected())
		safeErr := sanitizeJobError(herr)
		writeCtx := ctx
		if ctx.Err() != nil {
			writeCtx = context.Background()
		}
		if derr := p.db.WriteTx(writeCtx, func(tx *sql.Tx) error {
			return p.repo.MarkPermanentlyFailedWithLog(writeCtx, tx, j.ID, safeErr, logJSON)
		}); derr != nil {
			slog.Error("jobs.markpermfailed_withlog.err", "pool", p.name, "id", j.ID, "err", derr)
		}
		return
	}

	// Codex P5-07: over-limit (artifact / metadata) errors are PERMANENT
	// — the upstream is over the configured cap, so retrying with the
	// same input is guaranteed to fail again. Short-circuit to terminal
	// 'failed' status BYPASSING the retry ladder, mirroring the
	// HELMRETRY-03 partial-sync precedent above. Same ctx-cancel
	// fallback to context.Background() for shutdown-during-write.
	if errors.Is(herr, streamio.ErrArtifactTooLarge) || errors.Is(herr, streamio.ErrMetadataTooLarge) {
		safeErr := sanitizeJobError(herr)
		writeCtx := ctx
		if ctx.Err() != nil {
			writeCtx = context.Background()
		}
		if derr := p.db.WriteTx(writeCtx, func(tx *sql.Tx) error {
			return p.repo.MarkPermanentlyFailed(writeCtx, tx, j.ID, safeErr)
		}); derr != nil {
			slog.Error("jobs.markpermfailed.over_limit.err", "pool", p.name, "id", j.ID, "err", derr)
		}
		return
	}

	nextAttempts := int(j.Attempts) + 1
	// If ctx is already canceled (shutdown drain), don't try to update
	// the row — boot recovery will handle it next boot. Using a fresh
	// short-deadline context would still race against DB close. Simpler
	// and provably safe: bail.
	if ctx.Err() != nil {
		slog.Warn("jobs.markfailed.skipped.shutdown", "pool", p.name, "id", j.ID)
		return
	}
	// Sanitize + truncate handler error before it lands in sync_jobs.last_error
	// (plan 08-06 Codex rescue Q2). The raw error goes to slog for operators;
	// the client-visible last_error is scrubbed.
	safeErr := sanitizeJobError(herr)
	if nextAttempts >= MaxAttempts {
		if derr := p.db.WriteTx(ctx, func(tx *sql.Tx) error {
			return p.repo.MarkPermanentlyFailed(ctx, tx, j.ID, safeErr)
		}); derr != nil {
			slog.Error("jobs.markpermfailed.err", "pool", p.name, "id", j.ID, "err", derr)
		}
		return
	}
	nextRunAt := time.Now().Add(Backoff(nextAttempts))
	if derr := p.db.WriteTx(ctx, func(tx *sql.Tx) error {
		return p.repo.MarkFailed(ctx, tx, j.ID, safeErr, nextRunAt)
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
