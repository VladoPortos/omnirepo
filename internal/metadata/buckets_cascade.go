// Package metadata — internal/metadata/buckets_cascade.go
//
// s3_buckets cascade helpers used by ProjectsRepo.SoftDelete / Restore.
// No dedicated S3BucketsRepo struct exists in this package; bucket CRUD
// lives in internal/protocol/s3/backend. These helpers are
// project-cascade-only and live here so the ProjectsRepo cascade closure
// does not have to reach across package boundaries.
//
// Timestamp marker: the caller stamps `projects.deleted_at` first inside
// its WriteTx, reads the value back, and passes the literal string here
// — guaranteeing byte-for-byte equality on Restore so independently
// soft-deleted buckets (different timestamp) survive a project Restore.
package metadata

import (
	"context"
	"database/sql"
	"fmt"
)

// SoftDeleteAllBucketsForProject stamps deleted_at = cascadeTS on every live
// (deleted_at IS NULL) s3_buckets row for projectID. Returns the number of
// rows updated. Idempotent — already-soft-deleted rows are skipped.
func SoftDeleteAllBucketsForProject(ctx context.Context, tx *sql.Tx, projectID int64, cascadeTS string) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE s3_buckets
		   SET deleted_at = ?
		 WHERE project_id = ? AND deleted_at IS NULL
	`, cascadeTS, projectID)
	if err != nil {
		return 0, fmt.Errorf("s3_buckets: cascade soft-delete for project %d: %w", projectID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("s3_buckets: cascade soft-delete rows %d: %w", projectID, err)
	}
	return n, nil
}

// RestoreCascadedBucketsForProject clears deleted_at for buckets whose
// deleted_at exactly matches priorTS — i.e. only buckets cascade-soft-deleted
// by THIS project soft-delete. Buckets independently soft-deleted
// before the cascade are left as-is.
func RestoreCascadedBucketsForProject(ctx context.Context, tx *sql.Tx, projectID int64, priorTS string) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE s3_buckets
		   SET deleted_at = NULL
		 WHERE project_id = ? AND deleted_at = ?
	`, projectID, priorTS)
	if err != nil {
		return 0, fmt.Errorf("s3_buckets: restore cascaded for project %d: %w", projectID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("s3_buckets: restore cascaded rows %d: %w", projectID, err)
	}
	return n, nil
}
