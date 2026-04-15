package jobs

import (
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
