// Package jobs implements the two-pool (sync + scan) DB-leased job runner
// described by CONTEXT.md D-14..D-20.
package jobs

import (
	"math/rand"
	"sync"
	"time"
)

// MaxAttempts is the permanent-failure threshold. A job that fails on
// attempt >= MaxAttempts is marked permanently 'failed' and no longer
// retried (D-18, SYNC-04).
const MaxAttempts = 5

// backoffSchedule is the per-attempt delay table: attempts 1..5 map to
// 1m, 5m, 30m, 30m, 30m (D-18). Index with attempts-1 (clamped).
var backoffSchedule = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	30 * time.Minute,
	30 * time.Minute,
}

// jitterFraction is the ±bound applied to each backoff base value. ±10%
// is Claude-discretion default per CONTEXT.md "Claude's Discretion"
// section (avoids thundering herd on correlated failures without
// meaningfully stretching retry tail).
const jitterFraction = 0.10

// backoffRand supplies jitter for Backoff. math/rand.Rand is NOT safe for
// concurrent use, and Backoff is called from N worker goroutines on retry
// (see pool.markFailed), so all access goes through backoffRandMu.
var (
	backoffRandMu sync.Mutex
	backoffRand   = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// Backoff returns the next-run delay for the given attempt count (1-based:
// attempts==1 means "this was the first failure"). ±10% jitter is applied.
// Attempts <= 0 are treated as 1; attempts > len(schedule) clamp to the tail.
func Backoff(attempts int) time.Duration {
	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(backoffSchedule) {
		idx = len(backoffSchedule) - 1
	}
	base := backoffSchedule[idx]
	// Symmetric jitter in [-jitterFraction, +jitterFraction].
	backoffRandMu.Lock()
	r := backoffRand.Float64()
	backoffRandMu.Unlock()
	j := (r*2 - 1) * jitterFraction
	return base + time.Duration(float64(base)*j)
}
