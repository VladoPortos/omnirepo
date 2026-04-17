package jobs

import (
	"sync"
	"testing"
	"time"
)

func TestBackoffSchedule(t *testing.T) {
	cases := []struct {
		attempts int
		base     time.Duration
	}{
		{1, 1 * time.Minute},
		{2, 5 * time.Minute},
		{3, 30 * time.Minute},
		{4, 30 * time.Minute},
		{5, 30 * time.Minute},
		{6, 30 * time.Minute}, // clamp to tail
		{0, 1 * time.Minute},  // clamp to head
	}
	for _, c := range cases {
		d := Backoff(c.attempts)
		// Allow ±10% jitter.
		min := time.Duration(float64(c.base) * 0.9)
		max := time.Duration(float64(c.base) * 1.1)
		if d < min || d > max {
			t.Errorf("Backoff(%d)=%v outside [%v,%v]", c.attempts, d, min, max)
		}
	}
}

func TestBackoffJitterVaries(t *testing.T) {
	// Over 200 draws at attempts=3 (30m base), we should see at least 10
	// distinct duration values. Proves jitter is actually applied.
	seen := make(map[time.Duration]struct{})
	for i := 0; i < 200; i++ {
		seen[Backoff(3)] = struct{}{}
	}
	if len(seen) < 10 {
		t.Errorf("expected jitter to yield many distinct values, got %d", len(seen))
	}
}

func TestMaxAttemptsConst(t *testing.T) {
	if MaxAttempts != 5 {
		t.Fatalf("MaxAttempts=%d want 5", MaxAttempts)
	}
}

// TestBackoffConcurrent exercises Backoff from many goroutines to prove it is
// safe under -race. Worker pools call Backoff from separate goroutines on
// retry (pool.go: markFailed), and math/rand.Rand is not safe for concurrent
// use — without synchronization the race detector flags this loop.
func TestBackoffConcurrent(t *testing.T) {
	const (
		workers       = 16
		callsPerGroup = 200
	)
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < callsPerGroup; i++ {
				_ = Backoff((i % 6) + 1)
			}
		}()
	}
	wg.Wait()
}
