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
