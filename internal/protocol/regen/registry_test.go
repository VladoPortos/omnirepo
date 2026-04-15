package regen_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
)

func TestRegistryLazyCreate(t *testing.T) {
	t.Parallel()
	var factoryCalls atomic.Int64
	reg := regen.NewRegistry(10*time.Millisecond, 100*time.Millisecond, func(repoID int64) regen.RegenFn {
		factoryCalls.Add(1)
		return func(ctx context.Context) error { return nil }
	})
	defer func() { _ = reg.ShutdownAll(context.Background()) }()

	c1 := reg.Get(42)
	c2 := reg.Get(42)
	if c1 != c2 {
		t.Fatalf("Get(42) returned different pointers")
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory called %d times for single repo; want 1", got)
	}

	c3 := reg.Get(99)
	if c3 == c1 {
		t.Fatal("different repo ids must yield different coalescers")
	}
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("factory called %d times; want 2", got)
	}
}

func TestRegistryShutdownAll(t *testing.T) {
	t.Parallel()
	var running atomic.Int64
	reg := regen.NewRegistry(10*time.Millisecond, 50*time.Millisecond, func(repoID int64) regen.RegenFn {
		return func(ctx context.Context) error {
			running.Add(1)
			defer running.Add(-1)
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Millisecond):
			}
			return nil
		}
	})
	for _, id := range []int64{1, 2, 3} {
		c := reg.Get(id)
		c.Kick()
	}
	// Wait until at least one is in flight so ShutdownAll actually blocks.
	deadline := time.After(500 * time.Millisecond)
	for running.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("no regen ever started")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := reg.ShutdownAll(context.Background()); err != nil {
		t.Fatalf("shutdown all: %v", err)
	}
	if running.Load() != 0 {
		t.Fatalf("after shutdown running=%d want 0", running.Load())
	}
}

func TestRegistryConcurrentGetSameRepo(t *testing.T) {
	t.Parallel()
	var factoryCalls atomic.Int64
	reg := regen.NewRegistry(5*time.Millisecond, 50*time.Millisecond, func(repoID int64) regen.RegenFn {
		factoryCalls.Add(1)
		return func(ctx context.Context) error { return nil }
	})
	defer func() { _ = reg.ShutdownAll(context.Background()) }()

	const N = 32
	ptrs := make(chan *regen.Coalescer, N)
	for i := 0; i < N; i++ {
		go func() { ptrs <- reg.Get(7) }()
	}
	first := <-ptrs
	for i := 1; i < N; i++ {
		got := <-ptrs
		if got != first {
			t.Fatalf("concurrent Get returned different pointer")
		}
	}
	// Factory may have been called more than once due to the loser race,
	// but only one coalescer can survive in the map. We assert the
	// surviving pointer uniqueness (above) rather than factory count.
}

func TestRegistryDifferentReposFireIndependently(t *testing.T) {
	t.Parallel()
	var calls1, calls2 atomic.Int64
	reg := regen.NewRegistry(20*time.Millisecond, 200*time.Millisecond, func(repoID int64) regen.RegenFn {
		return func(ctx context.Context) error {
			if repoID == 1 {
				calls1.Add(1)
			} else {
				calls2.Add(1)
			}
			return nil
		}
	})
	defer func() { _ = reg.ShutdownAll(context.Background()) }()
	reg.Get(1).Kick()
	reg.Get(2).Kick()
	// Wait past debounce.
	time.Sleep(100 * time.Millisecond)
	if calls1.Load() != 1 || calls2.Load() != 1 {
		t.Fatalf("independent fire broken: c1=%d c2=%d", calls1.Load(), calls2.Load())
	}
}
