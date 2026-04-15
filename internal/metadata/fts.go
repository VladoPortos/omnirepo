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
