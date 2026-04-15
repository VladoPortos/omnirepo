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

// RecoveryReport summarizes a boot-recovery sweep.
type RecoveryReport struct {
	SyncRecovered  int
	ScansRecovered int
}

// RecoverStuckJobs re-pends sync_jobs and scans rows stuck in 'running'
// for more than StuckJobThreshold. Runs once at boot BEFORE any Pool
// dispatcher starts (so it cannot race with in-flight leases).
//
// Implemented as a single writer tx running two UPDATEs so both
// queues' recovery is all-or-nothing.
func RecoverStuckJobs(ctx context.Context, db *metadata.DB) (RecoveryReport, error) {
	var r RecoveryReport
	syncJobs := metadata.NewSyncJobsRepo(db)
	scans := metadata.NewScansRepo(db)
	olderThan := time.Now().Add(-StuckJobThreshold)
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
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
