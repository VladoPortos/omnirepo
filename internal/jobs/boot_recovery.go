package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// StuckJobThreshold is the age beyond which a 'running' row is
// considered abandoned and re-pended on boot (D-19, SYNC-03).
const StuckJobThreshold = 10 * time.Minute

// helmBootPartialLog is the partial-log JSON payload written for
// kind='helm_sync' rows recovered at boot (v1.5 Phase 5, HELMRETRY-03,
// D-07). Counts are null because boot recovery cannot know how many
// charts committed before the crash. Readers distinguish
// "boot-recovered" rows (nulls here) from "live-path recovered" rows
// (real counts written via MarkPermanentlyFailedWithLog) by checking
// whether files_persisted is null.
const helmBootPartialLog = `{"partial":true,"files_persisted":null,"files_expected":null}`

// RecoveryReport summarizes a boot-recovery sweep.
//
// HelmFailedTerminal counts stale kind='helm_sync' rows terminated at
// status='failed' by the helm-specific sweep (v1.5 Phase 5 D-02 / D-03b).
// SyncRecovered counts non-helm stale rows re-pended via the generic
// RecoverStale sweep. ScansRecovered counts stale scans rows re-pended.
type RecoveryReport struct {
	HelmFailedTerminal int
	SyncRecovered      int
	ScansRecovered     int
}

// RecoverStuckJobs sweeps rows stuck in 'running' for more than
// StuckJobThreshold. Runs once at boot BEFORE any Pool dispatcher starts
// (so it cannot race with in-flight leases).
//
// Implemented as a single writer tx running three UPDATEs so all
// queues' recovery is all-or-nothing:
//
//  1. helm_sync rows → status='failed' with a null-counts partial-log
//     payload (v1.5 Phase 5, HELMRETRY-03 D-02/D-03b). Must run FIRST
//     per Pitfall 4 ordering — otherwise the generic RecoverStale below
//     would re-pend helm rows before the helm-specific sweep sees them.
//  2. Remaining sync_jobs rows → status='pending' via the generic
//     RecoverStale (unchanged SYNC-03 / D-19 semantics; the helm sweep
//     above already consumed every matching helm row, so this one sees
//     non-helm kinds only).
//  3. scans rows → status='pending' via ScansRepo.RecoverStale.
func RecoverStuckJobs(ctx context.Context, db *metadata.DB) (RecoveryReport, error) {
	var r RecoveryReport
	syncJobs := metadata.NewSyncJobsRepo(db)
	scans := metadata.NewScansRepo(db)
	olderThan := time.Now().Add(-StuckJobThreshold)
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		// v1.5 Phase 5 (HELMRETRY-03, D-02, D-03b): helm-specific sweep
		// runs FIRST so the generic RecoverStale below never sees helm
		// rows. Ordering is load-bearing — reversing it would re-pend
		// helm rows before they could be terminated (Pitfall 4).
		hn, err := syncJobs.RecoverStaleByKind(ctx, tx, olderThan,
			"helm_sync", "failed", helmBootPartialLog)
		if err != nil {
			return fmt.Errorf("recover helm_sync: %w", err)
		}
		r.HelmFailedTerminal = hn

		// Generic sweep — sees non-helm rows only because the helm-
		// specific sweep above already consumed all stale helm rows.
		n, err := syncJobs.RecoverStale(ctx, tx, olderThan)
		if err != nil {
			return fmt.Errorf("recover sync_jobs: %w", err)
		}
		r.SyncRecovered = n

		n, err = scans.RecoverStale(ctx, tx, olderThan)
		if err != nil {
			return fmt.Errorf("recover scans: %w", err)
		}
		r.ScansRecovered = n
		return nil
	})
	return r, err
}
