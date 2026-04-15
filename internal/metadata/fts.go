// Package metadata FTS5 helpers (D-40, SRCH-01).
//
// Every helper takes a *sql.Tx so the FTS5 write runs inside the same
// writer transaction as the base-table mutation. Strong consistency: a
// search query executed after tx.Commit() will reflect the change
// immediately. No triggers, no async indexer.
//
// The three virtual tables (repos_fts, artifacts_fts, cves_fts) live in
// 001_initial.up.sql and use content='' (external content disabled). We
// manage rows by explicit INSERT/DELETE — FTS5's "external content"
// rebuild machinery is not in play.
package metadata

import (
	"context"
	"database/sql"
	"fmt"
)

// IndexRepo writes/updates the repos_fts row for repoID. Uses rowid =
// repoID (FTS5 supports rowid as the content key) so updates are
// DELETE + INSERT keyed by a stable identifier. Call inside the same tx
// as the repos-row mutation.
func IndexRepo(ctx context.Context, tx *sql.Tx, repoID int64, repoName, projectName, description, repoType string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM repos_fts WHERE rowid = ?`, repoID,
	); err != nil {
		return fmt.Errorf("fts: delete repos_fts rowid=%d: %w", repoID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repos_fts(rowid, repo_name, project_name, description, type)
		VALUES (?, ?, ?, ?, ?)
	`, repoID, repoName, projectName, description, repoType); err != nil {
		return fmt.Errorf("fts: insert repos_fts rowid=%d: %w", repoID, err)
	}
	return nil
}

// DeleteRepoFTS removes the repos_fts row for repoID.
func DeleteRepoFTS(ctx context.Context, tx *sql.Tx, repoID int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM repos_fts WHERE rowid = ?`, repoID,
	); err != nil {
		return fmt.Errorf("fts: delete repos_fts rowid=%d: %w", repoID, err)
	}
	return nil
}

// IndexArtifact appends an artifacts_fts row. Caller is responsible for
// deduping (call IndexArtifactDelete first if updating in place).
func IndexArtifact(ctx context.Context, tx *sql.Tx, repoID int64, name, version, digest string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts_fts(repo_id, name, version, digest)
		VALUES (?, ?, ?, ?)
	`, repoID, name, version, digest); err != nil {
		return fmt.Errorf("fts: insert artifacts_fts (repo=%d digest=%s): %w", repoID, digest, err)
	}
	return nil
}

// IndexArtifactDelete removes the artifacts_fts row matching
// (repo_id, digest).
func IndexArtifactDelete(ctx context.Context, tx *sql.Tx, repoID int64, digest string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM artifacts_fts WHERE repo_id = ? AND digest = ?`,
		repoID, digest,
	); err != nil {
		return fmt.Errorf("fts: delete artifacts_fts (repo=%d digest=%s): %w", repoID, digest, err)
	}
	return nil
}

// IndexVulnerability appends a cves_fts row. Caller is expected to call
// this once per new CVE id encountered (not once per vulnerability row
// — cves_fts is deduplicated at the cve_id level).
func IndexVulnerability(ctx context.Context, tx *sql.Tx, cveID, pkg, summary string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cves_fts(cve_id, package, summary) VALUES (?, ?, ?)
	`, cveID, pkg, summary); err != nil {
		return fmt.Errorf("fts: insert cves_fts %s: %w", cveID, err)
	}
	return nil
}

// -- Phase 3 Plan 01 (D-27): per-protocol FTS5 helpers. Each insert/delete
// -- runs inline in the caller's writer tx so a search executed after
// -- tx.Commit() reflects the change immediately. Tables live in migration
// -- 014_protocol_fts.up.sql and share the shape
// -- (repo_id UNINDEXED, name, version, arch_or_runtime, summary).

// IndexRPM inserts a row into rpm_fts. Caller runs IndexRPMDelete first if
// the underlying package is being updated in place (composite delete key).
func IndexRPM(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime, summary string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rpm_fts(repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, ?, ?, ?, ?)
	`, repoID, name, version, archOrRuntime, summary); err != nil {
		return fmt.Errorf("fts: insert rpm_fts: %w", err)
	}
	return nil
}

// IndexRPMDelete removes the rpm_fts row(s) keyed by
// (repo_id, name, version, arch_or_runtime).
func IndexRPMDelete(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM rpm_fts WHERE repo_id=? AND name=? AND version=? AND arch_or_runtime=?
	`, repoID, name, version, archOrRuntime); err != nil {
		return fmt.Errorf("fts: delete rpm_fts: %w", err)
	}
	return nil
}

// IndexDEB inserts a row into deb_fts.
func IndexDEB(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime, summary string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deb_fts(repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, ?, ?, ?, ?)
	`, repoID, name, version, archOrRuntime, summary); err != nil {
		return fmt.Errorf("fts: insert deb_fts: %w", err)
	}
	return nil
}

// IndexDEBDelete removes the deb_fts row(s) keyed by
// (repo_id, name, version, arch_or_runtime).
func IndexDEBDelete(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM deb_fts WHERE repo_id=? AND name=? AND version=? AND arch_or_runtime=?
	`, repoID, name, version, archOrRuntime); err != nil {
		return fmt.Errorf("fts: delete deb_fts: %w", err)
	}
	return nil
}

// IndexPyPI inserts a row into pypi_fts. archOrRuntime carries the
// requires-python string (or wheel tag) the UI wants to surface.
func IndexPyPI(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime, summary string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pypi_fts(repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, ?, ?, ?, ?)
	`, repoID, name, version, archOrRuntime, summary); err != nil {
		return fmt.Errorf("fts: insert pypi_fts: %w", err)
	}
	return nil
}

// IndexPyPIDelete removes the pypi_fts row(s) keyed by
// (repo_id, name, version, arch_or_runtime).
func IndexPyPIDelete(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM pypi_fts WHERE repo_id=? AND name=? AND version=? AND arch_or_runtime=?
	`, repoID, name, version, archOrRuntime); err != nil {
		return fmt.Errorf("fts: delete pypi_fts: %w", err)
	}
	return nil
}

// IndexHelm inserts a row into helm_fts. archOrRuntime carries appVersion.
func IndexHelm(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime, summary string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO helm_fts(repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, ?, ?, ?, ?)
	`, repoID, name, version, archOrRuntime, summary); err != nil {
		return fmt.Errorf("fts: insert helm_fts: %w", err)
	}
	return nil
}

// IndexHelmDelete removes the helm_fts row(s) keyed by
// (repo_id, name, version, arch_or_runtime).
func IndexHelmDelete(ctx context.Context, tx *sql.Tx, repoID int64, name, version, archOrRuntime string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM helm_fts WHERE repo_id=? AND name=? AND version=? AND arch_or_runtime=?
	`, repoID, name, version, archOrRuntime); err != nil {
		return fmt.Errorf("fts: delete helm_fts: %w", err)
	}
	return nil
}

// DeleteVulnerabilitiesByScan removes vulnerabilities rows for scanID and
// also removes cves_fts rows for CVEs that become orphaned (no other
// vulnerabilities row in any scan references them). Runs as a single
// unit inside the caller's tx.
func DeleteVulnerabilitiesByScan(ctx context.Context, tx *sql.Tx, scanID int64) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM cves_fts WHERE cve_id IN (
		    SELECT v.cve_id FROM vulnerabilities v
		    WHERE v.scan_id = ?
		      AND NOT EXISTS (
		          SELECT 1 FROM vulnerabilities v2
		          WHERE v2.cve_id = v.cve_id AND v2.scan_id <> v.scan_id
		      )
		)
	`, scanID); err != nil {
		return fmt.Errorf("fts: delete orphan cves_fts for scan %d: %w", scanID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM vulnerabilities WHERE scan_id = ?`, scanID,
	); err != nil {
		return fmt.Errorf("fts: delete vulnerabilities scan %d: %w", scanID, err)
	}
	return nil
}
