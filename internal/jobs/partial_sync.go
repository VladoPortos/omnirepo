package jobs

// Phase 5 Plan 03 HELMRETRY-03 — partial-sync error routing primitives.
//
// This file exists to resolve an import-cycle constraint: the helm package
// already imports internal/jobs (for JobView / Handler). internal/jobs
// cannot import the helm package back. But Pool.markFailed needs to
// recognise helm partial-sync errors and route them to the terminal-failed
// path.
//
// Resolution: define a narrow PartialSyncError interface here and match
// via errors.As against the interface type. The helm.PartialSyncErr
// concrete struct (defined alongside the helm sync handler) satisfies this
// interface structurally via its Persisted() / Expected() methods — no
// package-level dependency in either direction.
//
// D-03a live-path consumer: Pool.markFailed.
// D-03b boot-recovery consumer (Plan 04): RecoverStuckJobs.
//
// See .planning/phases/05-helm-retry-idempotency/05-RESEARCH.md §"Pool.markFailed branch"
// and 05-PATTERNS.md §"pool.go Planner decisions" for the full rationale.

import (
	"encoding/json"
	"fmt"
)

// HelmSyncKind is the sync_jobs.kind string the partial-sync routing in
// Pool.markFailed matches on. Duplicated here (not imported from the helm
// package) because internal/jobs MUST NOT import helm — helm already
// imports jobs, and importing back would create a cycle.
//
// If the constant ever diverges from helm.SyncJobKind it is caught by
// TestPool_HelmPartialSync_TerminalFailed (the test enqueues with
// "helm_sync" literally). v1.5 Phase 5 D-03a.
const HelmSyncKind = "helm_sync"

// PartialSyncError is a narrow interface that concrete partial-sync
// errors (e.g. *helm.PartialSyncErr) satisfy structurally. Using an
// interface rather than a concrete type import avoids an import cycle:
// the helm package already imports internal/jobs, so jobs cannot import
// helm back. The helm.PartialSyncErr struct already exposes the
// Persisted()/Expected() methods landed in Plan 01. v1.5 Phase 5 D-03a.
type PartialSyncError interface {
	Persisted() int64
	Expected() int64
}

// buildPartialLogJSON returns the canonical 3-field partial-log JSON
// payload used by the live path (Pool.markFailed). Shape is D-06-locked:
// {"partial":true,"files_persisted":N,"files_expected":M}. The boot
// recovery path (RecoverStuckJobs, Plan 04) writes a variant with null
// counts because progress is unknowable at boot.
func buildPartialLogJSON(persisted, expected int64) string {
	payload := struct {
		Partial        bool  `json:"partial"`
		FilesPersisted int64 `json:"files_persisted"`
		FilesExpected  int64 `json:"files_expected"`
	}{Partial: true, FilesPersisted: persisted, FilesExpected: expected}
	b, err := json.Marshal(payload)
	if err != nil {
		// Should never happen with a fixed-shape struct; fall back to a
		// safe literal so the DB UPDATE still carries valid JSON.
		return fmt.Sprintf(`{"partial":true,"files_persisted":%d,"files_expected":%d}`, persisted, expected)
	}
	return string(b)
}
