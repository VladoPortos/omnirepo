// Package jobs — ProgressWriter is the Phase 8 Plan 02 (M2.1) throttled
// helper protocol sync handlers use to advance sync_jobs.progress_bytes /
// total_bytes / current_step without hammering the writer pool.
//
// Throttle contract (D-12):
//   - Persist only when the (step, done, total) triple DIFFERS from the
//     last successfully-persisted triple AND at least 200 ms has elapsed
//     since the last successful persist.
//   - First call always persists (no prior state to compare against).
//   - Flush bypasses the throttle — always persists current values.
//
// Byte-level vs step-based (D-11):
//   - OCI / APT / RPM / PyPI: callers pass true byte totals; the UI renders
//     "N MiB / M MiB · X %".
//   - Helm: index.yaml lacks chart sizes, so total = 0 and done = chart
//     index (1-based). The UI falls back to "chart N of M · <filename>".
package jobs

import (
	"context"
	"sync"
	"time"
)

// ProgressMinInterval is the minimum wall-clock gap between persisted
// progress writes (D-12). A hot-loop io.Read callback can fire thousands
// of times per second; capping persists at 5 UPDATEs/sec/job keeps the
// writer pool from saturating under parallel sync loads.
const ProgressMinInterval = 200 * time.Millisecond

// MaxStepLen caps the size of current_step at the ProgressWriter boundary
// so upstream-controlled strings (package names, filenames) that reach the
// step via APT/RPM/PyPI/Helm cannot bloat SQLite with multi-megabyte rows.
// Plan 08-06 Codex rescue Q5: the step text embeds upstream-controlled
// fields like `pulling <Package>_<Version>` or `chart N of M · <filename>`,
// so a hostile upstream with a 10 MB package name could otherwise persist
// that verbatim. 1 KiB is generous enough for every realistic step shape
// (longest observed: helm `chart 999 of 999 · some-very-long-chart-name-
// with-vendor-prefix-1.2.3.tgz` ~80 bytes).
const MaxStepLen = 1024

// clampStep truncates step to MaxStepLen bytes on a UTF-8 boundary. Appends
// a "…" marker on truncation so the UI / operator sees the row was clamped
// rather than silently shortened. Uses byte truncation (not rune) because
// SQLite TEXT columns are byte-counted and a multi-byte rune at the boundary
// would otherwise land half-written.
func clampStep(step string) string {
	if len(step) <= MaxStepLen {
		return step
	}
	// Find the last UTF-8 start byte at or before MaxStepLen-4 (leaving 3
	// bytes for the "…" UTF-8 sequence + 1 byte headroom).
	cut := MaxStepLen - 4
	for cut > 0 && (step[cut]&0xC0) == 0x80 {
		cut--
	}
	return step[:cut] + "…"
}

// SyncProgressRepo is the narrow interface ProgressWriter calls.
// metadata.SyncJobsRepo satisfies it; tests supply a fake that records
// calls without a real DB.
//
// Defined locally (rather than importing metadata.SyncJobsRepo as a type)
// so the test harness can build a fake without pulling in a full
// metadata.DB — follows the same pattern as jobs.LeaseRepo in lease.go.
type SyncProgressRepo interface {
	SetProgress(ctx context.Context, jobID int64, step string, done, total int64) error
}

// ProgressWriter is a throttled wrapper around SyncProgressRepo.SetProgress.
// Concurrency: safe for multi-goroutine use (guarded by mu), though the
// intended call pattern is single-goroutine-per-job. The mutex is
// defensive — it ensures that if a later refactor hands a ProgressWriter
// to multiple download goroutines, the throttle + "last values" bookkeeping
// stay consistent without a code change here.
type ProgressWriter struct {
	repo  SyncProgressRepo
	jobID int64

	// now is injectable so tests can advance a fake clock deterministically.
	// Default: time.Now.
	now func() time.Time

	mu        sync.Mutex
	lastAt    time.Time
	lastStep  string
	lastDone  int64
	lastTotal int64
	dirty     bool // true after first successful persist
}

// NewProgressWriter builds a writer bound to repo + jobID. The returned
// writer is zero-allocation for the no-op / throttled paths — only the
// actual Set() that passes the throttle gate performs a DB write.
func NewProgressWriter(repo SyncProgressRepo, jobID int64) *ProgressWriter {
	return &ProgressWriter{repo: repo, jobID: jobID, now: time.Now}
}

// SetNow overrides the clock source — reserved for tests in the same
// package. External callers should rely on the default time.Now.
func (p *ProgressWriter) SetNow(now func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	p.now = now
}

// Set persists (step, done, total) subject to the throttle contract.
//
// Returns:
//   - nil on throttled / no-change suppressions (by design — callers do
//     not need to distinguish a suppressed write from a successful one).
//   - the underlying SetProgress error verbatim when the DB write fails.
//     Callers typically log-and-continue: a failed progress write must
//     not abort the sync.
//
// Even when the DB write is throttle-suppressed, Set ALWAYS updates the
// in-memory (lastStep, lastDone, lastTotal) triple. This guarantees that
// a subsequent Flush emits the caller's most-recent intended values —
// not a stale "layer 1 of 7" when the handler already emitted a "done"
// sentinel that hit the 200 ms gate. Without this, a fast sync that
// finishes within 200 ms would persist a misleading non-terminal step.
//
// If repo is nil (e.g. legacy call site that hasn't been wired yet),
// Set is a no-op returning nil. This lets protocol handlers defensively
// construct a ProgressWriter even when the sync-jobs repo isn't plumbed
// through in tests.
func (p *ProgressWriter) Set(ctx context.Context, step string, done, total int64) error {
	if p == nil || p.repo == nil {
		return nil
	}
	// Clamp upstream-controlled step text at the boundary (Codex Q5).
	step = clampStep(step)
	p.mu.Lock()
	defer p.mu.Unlock()
	// No-change: skip without updating last* (values are already current).
	if p.dirty && step == p.lastStep && done == p.lastDone && total == p.lastTotal {
		return nil
	}
	// Always remember the caller's most-recent intended triple so Flush
	// emits it even if the DB write is throttled below.
	p.lastStep, p.lastDone, p.lastTotal = step, done, total
	now := p.now()
	if p.dirty && now.Sub(p.lastAt) < ProgressMinInterval {
		return nil // throttled — state captured, will be flushed.
	}
	if err := p.repo.SetProgress(ctx, p.jobID, step, done, total); err != nil {
		return err
	}
	p.lastAt = now
	p.dirty = true
	return nil
}

// Flush bypasses the throttle and unconditionally persists the last-known
// (step, done, total) triple. Protocol handlers call Flush once at the
// end of a sync so the final step is visible to UI pollers even if the
// last Set(...) was throttle-suppressed.
//
// Flush does NOT attempt to re-derive a "final" state — it writes what
// Set was last called with (including a throttle-suppressed Set, since
// Set still updates the in-memory triple in that case). Callers that
// need a sentinel like "done" should Set("done", total, total) just
// before the handler returns; defer progress.Flush(ctx) guarantees the
// sentinel lands.
func (p *ProgressWriter) Flush(ctx context.Context) error {
	if p == nil || p.repo == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty && p.lastStep == "" && p.lastDone == 0 && p.lastTotal == 0 {
		// Nothing was ever set (truly pristine); nothing to flush.
		return nil
	}
	if err := p.repo.SetProgress(ctx, p.jobID, p.lastStep, p.lastDone, p.lastTotal); err != nil {
		return err
	}
	p.lastAt = p.now()
	p.dirty = true
	return nil
}
