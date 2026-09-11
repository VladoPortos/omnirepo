package auth_test

import (
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/auth"
)

func TestAttemptLimiter_BoundsPeerRateAndRefills(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	limiter := auth.NewAttemptLimiter(auth.AttemptLimiterConfig{
		Burst: 2, RefillInterval: time.Minute, MaxPeers: 8, MaxConcurrent: 2,
		Clock: func() time.Time { return now },
	})

	for i := 0; i < 2; i++ {
		permit, retry, ok := limiter.Acquire("192.0.2.10:1234")
		if !ok || permit == nil || retry != 0 {
			t.Fatalf("attempt %d: ok=%v permit=%v retry=%v", i, ok, permit, retry)
		}
		permit.Release()
	}
	if permit, retry, ok := limiter.Acquire("192.0.2.10:9999"); ok || permit != nil || retry != time.Minute {
		t.Fatalf("exhausted: ok=%v permit=%v retry=%v", ok, permit, retry)
	}

	now = now.Add(time.Minute)
	permit, retry, ok := limiter.Acquire("192.0.2.10:7777")
	if !ok || permit == nil || retry != 0 {
		t.Fatalf("refilled: ok=%v permit=%v retry=%v", ok, permit, retry)
	}
	permit.Release()
}

func TestAttemptLimiter_BoundsConcurrentWork(t *testing.T) {
	limiter := auth.NewAttemptLimiter(auth.AttemptLimiterConfig{
		Burst: 10, RefillInterval: time.Minute, MaxPeers: 8, MaxConcurrent: 1,
	})
	first, _, ok := limiter.Acquire("192.0.2.1:1")
	if !ok {
		t.Fatal("first acquire rejected")
	}
	if permit, retry, ok := limiter.Acquire("192.0.2.2:2"); ok || permit != nil || retry <= 0 {
		t.Fatalf("concurrent acquire: ok=%v permit=%v retry=%v", ok, permit, retry)
	}
	first.Release()
	first.Release() // release is intentionally idempotent for middleware safety.
	if permit, _, ok := limiter.Acquire("192.0.2.2:2"); !ok || permit == nil {
		t.Fatal("permit not returned after release")
	} else {
		permit.Release()
	}
}

func TestAttemptLimiter_EvictsOldestPeerAtBound(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	limiter := auth.NewAttemptLimiter(auth.AttemptLimiterConfig{
		Burst: 1, RefillInterval: time.Hour, MaxPeers: 2, MaxConcurrent: 2,
		Clock: func() time.Time { return now },
	})
	for _, peer := range []string{"192.0.2.1:1", "192.0.2.2:2"} {
		permit, _, ok := limiter.Acquire(peer)
		if !ok {
			t.Fatalf("acquire %s rejected", peer)
		}
		permit.Release()
		now = now.Add(time.Second)
	}
	permit, _, ok := limiter.Acquire("192.0.2.3:3")
	if !ok {
		t.Fatal("third peer rejected")
	}
	permit.Release()
	if got := limiter.PeerCount(); got != 2 {
		t.Fatalf("PeerCount=%d want 2", got)
	}
	permit, _, ok = limiter.Acquire("192.0.2.1:55")
	if !ok {
		t.Fatal("oldest peer was not evicted")
	}
	permit.Release()
}
