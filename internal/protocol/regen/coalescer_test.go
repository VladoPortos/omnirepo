package regen_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/protocol/regen"
)

func TestCoalescerDebounceCollapses(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	c := regen.New(50*time.Millisecond, 1*time.Second, func(ctx context.Context) error {
		calls.Add(1)
		return nil
	})
	defer func() { _ = c.Shutdown(context.Background()) }()
	for i := 0; i < 100; i++ {
		c.Kick()
	}
	// Wait past the debounce window.
	time.Sleep(200 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls=%d want 1 (debounce must collapse bursts)", got)
	}
}

func TestCoalescerMaxWaitFires(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	c := regen.New(100*time.Millisecond, 300*time.Millisecond, func(ctx context.Context) error {
		calls.Add(1)
		return nil
	})
	defer func() { _ = c.Shutdown(context.Background()) }()
	// Kick continuously every 40ms (debounce/2ish) so the debounce timer
	// never elapses; maxWait must still fire within ~300ms.
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				c.Kick()
			}
		}
	}()
	// Observe at least one fire inside maxWait + slack.
	deadline := time.After(1 * time.Second)
	for calls.Load() < 1 {
		select {
		case <-deadline:
			close(stop)
			t.Fatalf("maxWait did not fire: calls=%d", calls.Load())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	close(stop)
}

func TestCoalescerShutdownWaitsForInflight(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var done atomic.Bool
	c := regen.New(10*time.Millisecond, 100*time.Millisecond, func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		done.Store(true)
		return nil
	})
	c.Kick()
	<-started
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- c.Shutdown(context.Background()) }()
	// Shutdown must NOT return while fn is in flight.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while fn still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown err: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("shutdown did not return after release")
	}
	if !done.Load() {
		t.Fatal("fn never finished")
	}
}

func TestCoalescerShutdownCtxDeadline(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	defer close(release)
	c := regen.New(10*time.Millisecond, 100*time.Millisecond, func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	c.Kick()
	// Wait for fn to actually start.
	deadline := time.After(500 * time.Millisecond)
	for !c.Inflight() {
		select {
		case <-deadline:
			t.Fatal("fn never started")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Shutdown with a tight ctx: should return ctx.Err().
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := c.Shutdown(ctx)
	if err == nil {
		t.Fatal("Shutdown should have returned ctx error")
	}
}

func TestCoalescerConcurrentKicksSingleFire(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	c := regen.New(30*time.Millisecond, 1*time.Second, func(ctx context.Context) error {
		calls.Add(1)
		return nil
	})
	defer func() { _ = c.Shutdown(context.Background()) }()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.Kick()
			}
		}()
	}
	wg.Wait()
	time.Sleep(120 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls=%d want 1 under concurrent kicks", got)
	}
}
