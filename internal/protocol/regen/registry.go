package regen

import (
	"context"
	"sync"
	"time"
)

// FactoryFn constructs the RegenFn for a given repo id. app.Run wires
// this at construction; it typically closes over the protocol handlers,
// the signing-keys repo, and the config.
type FactoryFn func(repoID int64) RegenFn

// Registry is the per-repo lazy Coalescer store. Callers fetch (or
// create) the coalescer for a repo via Get(repoID).Kick() — no
// construction bookkeeping at the call site.
type Registry struct {
	debounce time.Duration
	maxWait  time.Duration
	factory  FactoryFn

	m sync.Map // int64 -> *Coalescer
}

// NewRegistry constructs a Registry. debounce/maxWait feed each new
// Coalescer; factory materializes a RegenFn from repo id.
func NewRegistry(debounce, maxWait time.Duration, factory FactoryFn) *Registry {
	if factory == nil {
		panic("regen: nil factory")
	}
	return &Registry{debounce: debounce, maxWait: maxWait, factory: factory}
}

// Get returns the Coalescer for repoID, creating it on first call.
// Concurrent-safe: two goroutines racing on a cold cache both receive
// the same pointer.
func (r *Registry) Get(repoID int64) *Coalescer {
	if v, ok := r.m.Load(repoID); ok {
		return v.(*Coalescer)
	}
	c := New(r.debounce, r.maxWait, r.factory(repoID))
	actual, loaded := r.m.LoadOrStore(repoID, c)
	if loaded {
		// Lost the race; shut down the duplicate to reclaim its goroutine.
		_ = c.Shutdown(context.Background())
		return actual.(*Coalescer)
	}
	return c
}

// ShutdownAll invokes Shutdown on every live coalescer and waits for
// them to drain. Returns the first non-nil error (typically ctx
// cancellation). Safe to call concurrently with Get — coalescers that
// appear after the iteration starts are left to their own lifecycle.
func (r *Registry) ShutdownAll(ctx context.Context) error {
	var firstErr error
	r.m.Range(func(_, v any) bool {
		c := v.(*Coalescer)
		if err := c.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		return true
	})
	return firstErr
}
