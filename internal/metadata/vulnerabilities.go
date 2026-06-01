package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// VulnerabilitiesRepo owns vulnerabilities rows. Parsed Trivy output is
// batch-inserted; the caller enforces a per-scan cap (default
// 10000 in the scan handler).
type VulnerabilitiesRepo struct{ db *DB }

// Vuln is the in-memory projection of a vulnerabilities row (input to
// InsertBatch; id is not set on input).
type Vuln struct {
	CVEID          string
	Severity       string
	PackageName    string
	PackageVersion string
	FixedVersion   string
	Title          string
	Description    string
}

// ErrVulnBatchTooLarge is returned by InsertBatch when the batch exceeds
// the caller-provided cap.
var ErrVulnBatchTooLarge = errors.New("vulnerabilities: batch over cap")

// NewVulnerabilitiesRepo constructs a repo bound to db.
func NewVulnerabilitiesRepo(db *DB) *VulnerabilitiesRepo { return &VulnerabilitiesRepo{db: db} }

// InsertBatch appends up to cap vulnerability rows for scanID in a single
// tx (caller-supplied). Returns ErrVulnBatchTooLarge if len(vulns) > cap.
// When cap <= 0 it is treated as "no cap".
func (r *VulnerabilitiesRepo) InsertBatch(ctx context.Context, tx *sql.Tx, scanID int64, vulns []Vuln, cap int) error {
	if cap > 0 && len(vulns) > cap {
		return fmt.Errorf("%w: %d > %d", ErrVulnBatchTooLarge, len(vulns), cap)
	}
	if len(vulns) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO vulnerabilities(scan_id, cve_id, severity, package_name, package_version, fixed_version, title, description)
		VALUES (?,?,?,?,?,?,?,?)
	`)
	if err != nil {
		return fmt.Errorf("vulnerabilities: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, v := range vulns {
		if _, err := stmt.ExecContext(ctx, scanID, v.CVEID, v.Severity, v.PackageName, v.PackageVersion, v.FixedVersion, v.Title, v.Description); err != nil {
			return fmt.Errorf("vulnerabilities: insert %s: %w", v.CVEID, err)
		}
	}
	return nil
}

// DeleteByScan removes every vulnerability row for scanID. Used by
// rescan flows before re-inserting the fresh batch.
func (r *VulnerabilitiesRepo) DeleteByScan(ctx context.Context, tx *sql.Tx, scanID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM vulnerabilities WHERE scan_id=?`, scanID)
	if err != nil {
		return fmt.Errorf("vulnerabilities: delete_by_scan %d: %w", scanID, err)
	}
	return nil
}

// CountByScan returns how many rows exist for scanID. Useful in tests
// and the REST envelope.
func (r *VulnerabilitiesRepo) CountByScan(ctx context.Context, scanID int64) (int, error) {
	var n int
	if err := r.db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vulnerabilities WHERE scan_id=?`, scanID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("vulnerabilities: count: %w", err)
	}
	return n, nil
}
