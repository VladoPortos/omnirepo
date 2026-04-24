package helm

// Phase 5 Plan 01 HELMRETRY-03 — typed partial-sync error.
//
// Introduces the error class that SyncHandler.Handle returns when a sync
// is interrupted before every upstream chart is committed to helm_charts
// + storage. Two interruption shapes produce this error:
//
//  1. ctx cancellation mid-flight (D-03a): the jobs pool ticked down its
//     deadline / the parent cancelled; some goroutines may still commit
//     before wg.Wait() returns, so the persisted count is whatever
//     filesAdded carries under the mutex at that point.
//
//  2. upstream 500 (or any download error) before all charts persisted:
//     the first download error is wrapped as the cause so callers can
//     still reach the sanitised upstream error via errors.Unwrap.
//
// Both paths let callers identify the class via errors.Is(err,
// ErrHelmPartialSync) and read counts via errors.As(err, &pse). Plan 03
// will consume this from internal/jobs via a narrow interface (no direct
// import) so the jobs pool can route partial syncs to the terminal-failed
// path (D-01, D-03a).

import (
	"errors"
	"fmt"
)

// ErrHelmPartialSync is the sentinel every partial-sync error unwraps to.
// Callers use errors.Is(err, ErrHelmPartialSync) to test the class without
// needing the counts; use errors.As(err, &pse) to read Persisted/Expected.
var ErrHelmPartialSync = errors.New("helm_sync: partial — sync interrupted before all charts committed")

// PartialSyncErr carries per-sync counts (files_persisted / files_expected)
// through jobs.Pool.markFailed for terminal-failed routing. Exported so
// internal/jobs can errors.As on the concrete type (via a narrow interface
// defined there — see Plan 03).
type PartialSyncErr struct {
	persisted int64
	expected  int64
	// cause is the optional wrapped cause (e.g. the first upstream-500
	// download error). nil for pure ctx-cancel partial syncs.
	cause error
}

// newPartialSyncErr constructs a partial-sync error with the given counts
// and an optional wrapped cause.
func newPartialSyncErr(persisted, expected int64, cause error) *PartialSyncErr {
	return &PartialSyncErr{persisted: persisted, expected: expected, cause: cause}
}

// Persisted returns the number of charts successfully committed to
// helm_charts + storage before the sync was interrupted.
func (e *PartialSyncErr) Persisted() int64 { return e.persisted }

// Expected returns the total number of charts the upstream index.yaml
// declared for this sync (len(entries) after ParseUpstream).
func (e *PartialSyncErr) Expected() int64 { return e.expected }

// Error returns a human-readable description including the counts and
// (if present) the underlying cause.
func (e *PartialSyncErr) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s (persisted=%d expected=%d): %v",
			ErrHelmPartialSync.Error(), e.persisted, e.expected, e.cause)
	}
	return fmt.Sprintf("%s (persisted=%d expected=%d)",
		ErrHelmPartialSync.Error(), e.persisted, e.expected)
}

// Unwrap makes errors.Is(err, ErrHelmPartialSync) succeed AND (when cause
// is set) lets the chain reach the wrapped upstream error too. Multi-arg
// Unwrap requires Go 1.20+.
func (e *PartialSyncErr) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrHelmPartialSync}
	}
	return []error{ErrHelmPartialSync, e.cause}
}
