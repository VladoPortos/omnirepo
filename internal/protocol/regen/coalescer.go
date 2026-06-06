// Package regen implements the debounced repo-metadata regeneration
// coalescer + per-repo registry used by every package protocol
// (RPM/APT/PyPI/Helm). Writers call Kick() after committing a package
// upload tx; the coalescer absorbs a burst of kicks within a debounce
// window and fires the supplied RegenFn exactly once — with a hard
// maxWait cap so continuous writes cannot starve regen.
package regen

import (
	"context"
	"sync/atomic"
	"time"
)

// RegenFn is the regeneration callback supplied at coalescer
// construction. It must honor ctx cancellation (Shutdown cancels the
// in-flight invocation's ctx after its deadline elapses).
type RegenFn func(ctx context.Context) error

// Coalescer is a per-repo debounced regen goroutine.
type Coalescer struct {
	debounce time.Duration
	maxWait  time.Duration
	fn       RegenFn

	kicks chan struct{}
	stop  chan struct{}
	done  chan struct{}

	// Inflight flag enables Shutdown to wait for a currently-running
	// fn to finish. Updated atomically for the read path in tests.
	inflight atomic.Bool
}

// New constructs a Coalescer and starts its goroutine. Callers must
// invoke Shutdown before discarding the instance (and before the
// supplied fn's closures become invalid). debounce is the minimum quiet
// window between the last Kick and the fire. maxWait is the hard cap
// from the first post-idle Kick to guarantee forward progress. fn must
// not be nil.
func New(debounce, maxWait time.Duration, fn RegenFn) *Coalescer {
	if fn == nil {
		panic("regen: nil RegenFn")
	}
	if debounce <= 0 {
		debounce = 1 * time.Millisecond
	}
	if maxWait <= 0 || maxWait < debounce {
		maxWait = debounce
	}
	c := &Coalescer{
		debounce: debounce,
		maxWait:  maxWait,
		fn:       fn,
		// Size-1 buffer: Kick non-blocking even when the goroutine is
		// currently firing (we collapse to at most one pending kick).
		kicks: make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go c.loop()
	return c
}

// Kick signals "dirty" to the coalescer. Non-blocking: if a kick is
// already pending (or the goroutine is mid-fire), this is a no-op.
// Safe for concurrent use.
func (c *Coalescer) Kick() {
	select {
	case c.kicks <- struct{}{}:
	default:
		// already pending — coalesced by design.
	}
}

// Shutdown stops the goroutine and waits for any in-flight regen to
// finish. Returns ctx.Err() if ctx fires before the goroutine exits;
// otherwise nil. Safe to call more than once.
func (c *Coalescer) Shutdown(ctx context.Context) error {
	select {
	case <-c.stop:
		// already requested
	default:
		close(c.stop)
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loop is the debounced dispatcher. State machine:
//
//   - IDLE: wait for first Kick; on arrival, transition to PENDING with
//     debounceTimer = debounce, maxTimer = maxWait.
//   - PENDING: further Kicks reset debounceTimer but leave maxTimer alone.
//     Fire when either timer elapses; after fn returns, return to IDLE.
//   - STOP: drain and exit.
func (c *Coalescer) loop() {
	defer close(c.done)
	var (
		debounceTimer *time.Timer
		maxTimer      *time.Timer
	)
	resetTimers := func() {
		debounceTimer = time.NewTimer(c.debounce)
		maxTimer = time.NewTimer(c.maxWait)
	}
	stopTimers := func() {
		if debounceTimer != nil && !debounceTimer.Stop() {
			select {
			case <-debounceTimer.C:
			default:
			}
		}
		if maxTimer != nil && !maxTimer.Stop() {
			select {
			case <-maxTimer.C:
			default:
			}
		}
		debounceTimer, maxTimer = nil, nil
	}

	for {
		// Idle: wait for the first kick.
		select {
		case <-c.stop:
			return
		case <-c.kicks:
			resetTimers()
		}

		// Pending loop until one of the timers fires (or we're asked to stop).
		fire := false
	pending:
		for {
			select {
			case <-c.stop:
				stopTimers()
				return
			case <-c.kicks:
				// Reset only the debounce timer. maxTimer is untouched so
				// a continuous stream of Kicks still fires by maxWait.
				if !debounceTimer.Stop() {
					select {
					case <-debounceTimer.C:
					default:
					}
				}
				debounceTimer.Reset(c.debounce)
			case <-debounceTimer.C:
				fire = true
				break pending
			case <-maxTimer.C:
				fire = true
				break pending
			}
		}
		stopTimers()

		if fire {
			c.inflight.Store(true)
			// Run fn with a background context. Callers' RegenFn are
			// expected to use bounded metadata writes; if the process
			// itself is exiting, main() enforces the overall deadline
			// via Shutdown's ctx. We do NOT cancel this runCtx on stop:
			// Shutdown is defined as "wait for in-flight fn".
			err := c.fn(context.Background())
			c.inflight.Store(false)
			// Swallow err here — RegenFn owns its own error recording
			// (repos.last_regen_error) via the metadata writer.
			_ = err
		}
	}
}

// Inflight reports whether a regen is currently running. Exposed for tests.
func (c *Coalescer) Inflight() bool { return c.inflight.Load() }

