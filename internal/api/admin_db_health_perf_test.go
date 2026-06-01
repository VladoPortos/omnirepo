//go:build perf500

// CI-only 500-MB perf gate for GET /api/v1/admin/db/health — the literal
// spec-budget assertion. Invoked via `make test-perf`, not as part
// of the default `go test ./...` run, so the fast merge-gate (10-MB
// proxy in admin_db_health_test.go) stays fast.
//
// Why this file is tagged: generating a 500-MB fixture takes ~1-5 s on a
// warm SSD and tens of seconds on slow disks. Rolling that cost into
// every `make test` invocation would slow the merge-gate noticeably for
// a gate that only needs to fire in CI / release pipelines.
//
// Linear-extrapolation defense: the handler performs only O(1) work per
// request — 3 cached settings reads, 3 O(1) PRAGMAs on the SQLite
// header, 2 os.Stat calls. The 10-MB proxy in admin_db_health_test.go
// catches O(n) regressions (an accidental integrity_check, an O(n) scan
// on freelist_count, etc.) before they escalate; this 500-MB test pins
// the literal spec budget so the gate cannot be gamed by tightening the
// proxy below what the spec asserts.
package api_test

import (
	"net/http"
	"sort"
	"testing"
	"time"
)

func TestAdminDBHealth_PerfBudget_500MB(t *testing.T) {
	const targetBytes = 500 * 1024 * 1024 // 500 MB per spec budget.

	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// growDBForPerfTest is defined in admin_db_health_test.go (same
	// package, untagged build). Reused here so disk-growth logic has a
	// single implementation.
	growDBForPerfTest(t, s, targetBytes)

	var times []time.Duration
	for i := 0; i < 10; i++ {
		start := time.Now()
		resp, _ := s.do(t, "GET", "/api/v1/admin/db/health", cookie, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("iter %d code=%d", i, resp.StatusCode)
		}
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	// Index 9 of a 10-sample run = max; we treat it as a conservative
	// p95 proxy (any sample > budget fails the gate).
	p95 := times[9]
	if p95 > 100*time.Millisecond {
		t.Fatalf("GET /admin/db/health p95 %v exceeds 100 ms budget on 500 MB DB", p95)
	}
	t.Logf("500MB perf: p95=%v p50=%v (samples=%d)", p95, times[len(times)/2], len(times))
}
