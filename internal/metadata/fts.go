// Package metadata FTS5 helpers (D-40, SRCH-01).
//
// Every helper takes a *sql.Tx so the FTS5 write runs inside the same
// writer transaction as the base-table mutation. Strong consistency: a
// search query executed after tx.Commit() will reflect the change
// immediately. No triggers, no async indexer.
//
// The three virtual tables (repos_fts, artifacts_fts, cves_fts) live in
// 001_initial.up.sql and use content=” (external content disabled). We
// manage rows by explicit INSERT/DELETE — FTS5's "external content"
// rebuild machinery is not in play.
package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

// PruneRepoFTS removes every FTS5 row referencing repoID from the per-repo
// tables (repos_fts by rowid; artifacts_fts, rpm_fts, deb_fts, pypi_fts,
// helm_fts by repo_id), and conditionally prunes cves_fts (D-11): a CVE is
// removed only when every vulnerability referencing it chains to repoID —
// i.e. no live repo still owns it.
//
// The cves_fts prune walks vulnerabilities.scan_id → scans.id → scans.repo_id;
// this is the only chain available because vulnerabilities has no direct
// repo_id column (verified against migrations/002_jobs.up.sql). A CVE
// co-referenced by any live repo (deleted_at IS NULL) survives, so
// loss-free reindex on Restore is preserved at the cves_fts layer.
//
// Idempotent: re-running on an already-pruned repo is a no-op (DELETE
// matches zero rows across every clause).
func PruneRepoFTS(ctx context.Context, tx *sql.Tx, repoID int64) error {
	if err := DeleteRepoFTS(ctx, tx, repoID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts_fts WHERE repo_id=?`, repoID); err != nil {
		return fmt.Errorf("fts: prune artifacts_fts (repo=%d): %w", repoID, err)
	}
	for _, table := range []string{"rpm_fts", "deb_fts", "pypi_fts", "helm_fts"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE repo_id=?`, repoID); err != nil {
			return fmt.Errorf("fts: prune %s (repo=%d): %w", table, repoID, err)
		}
	}
	// Conditional cves_fts prune (D-11). Delete cves_fts rows whose every
	// chain reaches repoID and no other live repo. Verbatim SQL — chained
	// through scans because vulnerabilities has no direct repo_id column.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM cves_fts
		 WHERE cve_id IN (
		   SELECT v.cve_id FROM vulnerabilities v
		     INNER JOIN scans s ON s.id = v.scan_id
		    WHERE s.repo_id = ?
		      AND v.cve_id NOT IN (
		        SELECT v2.cve_id FROM vulnerabilities v2
		          INNER JOIN scans s2 ON s2.id = v2.scan_id
		          INNER JOIN repos  r2 ON r2.id = s2.repo_id
		         WHERE s2.repo_id != ?
		           AND r2.deleted_at IS NULL
		      )
		 )
	`, repoID, repoID); err != nil {
		return fmt.Errorf("fts: prune cves_fts (repo=%d): %w", repoID, err)
	}
	return nil
}

// FTSReindexer walks canonical base tables for a repo and re-populates FTS5.
// Used by Repos.Restore + Projects.Restore (LIFECYCLE-09). Construction takes
// the four per-protocol typed package repos so ReindexRepo can call their
// ListByRepo helpers; raw_files + docker_manifests are read directly via the
// caller's tx since they have no Go-typed Repo struct hot path here.
type FTSReindexer struct {
	db       *DB
	rpmRepo  *RPMPackagesRepo
	debRepo  *DEBPackagesRepo
	pypiRepo *PyPIFilesRepo
	helmRepo *HelmChartsRepo
}

// NewFTSReindexer constructs a reindexer wired to the canonical base-table
// repos. Pass nil for any repo whose ListByRepo is not needed in tests; the
// nil branch is silently skipped (callers in production wire all four).
func NewFTSReindexer(db *DB, rpm *RPMPackagesRepo, deb *DEBPackagesRepo, pypi *PyPIFilesRepo, helm *HelmChartsRepo) *FTSReindexer {
	return &FTSReindexer{db: db, rpmRepo: rpm, debRepo: deb, pypiRepo: pypi, helmRepo: helm}
}

// ReindexRepo re-derives every FTS5 row for repoID from the base tables.
// Caller must have already pruned (or the FTS5 must be empty for repoID) —
// ReindexRepo INSERTs only, no DELETEs. Repo metadata (repos_fts) is fetched
// via repos+projects join inside.
//
// cves_fts is NOT reindexed here: vulnerabilities rows are untouched by
// SoftDelete, so any CVE that PruneRepoFTS removed (because it was exclusive
// to this repo) will be re-derived by the SAME chain on the next scan run.
// CVEs co-referenced by live repos were never removed — they remain indexed
// throughout the soft-delete window.
func (rx *FTSReindexer) ReindexRepo(ctx context.Context, tx *sql.Tx, repoID int64) error {
	// (a) repos_fts metadata.
	var repoName, projectName, description, repoType string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.name, p.name, r.description_md, r.type
		  FROM repos r INNER JOIN projects p ON p.id = r.project_id
		 WHERE r.id = ?
	`, repoID).Scan(&repoName, &projectName, &description, &repoType); err != nil {
		return fmt.Errorf("fts: reindex repo metadata lookup (repo=%d): %w", repoID, err)
	}
	if err := IndexRepo(ctx, tx, repoID, repoName, projectName, description, repoType); err != nil {
		return err
	}

	// (b) rpm_packages → rpm_fts.
	if rx.rpmRepo != nil {
		rpms, err := rx.rpmRepo.ListByRepo(ctx, repoID)
		if err != nil {
			return fmt.Errorf("fts: reindex list rpms (repo=%d): %w", repoID, err)
		}
		for _, p := range rpms {
			if err := IndexRPM(ctx, tx, repoID, p.Name, p.Version, p.Arch, p.Summary); err != nil {
				return err
			}
		}
	}
	// (c) deb_packages → deb_fts (firstLine of Description).
	if rx.debRepo != nil {
		debs, err := rx.debRepo.ListByRepo(ctx, repoID)
		if err != nil {
			return fmt.Errorf("fts: reindex list debs (repo=%d): %w", repoID, err)
		}
		for _, p := range debs {
			first := p.Description
			if i := strings.IndexByte(first, '\n'); i >= 0 {
				first = first[:i]
			}
			if err := IndexDEB(ctx, tx, repoID, p.Package, p.Version, p.Architecture, first); err != nil {
				return err
			}
		}
	}
	// (d) pypi_files → pypi_fts.
	if rx.pypiRepo != nil {
		pypis, err := rx.pypiRepo.ListByRepo(ctx, repoID)
		if err != nil {
			return fmt.Errorf("fts: reindex list pypi (repo=%d): %w", repoID, err)
		}
		for _, f := range pypis {
			if err := IndexPyPI(ctx, tx, repoID, f.ProjectNormalized, f.Version, f.RequiresPython, ""); err != nil {
				return err
			}
		}
	}
	// (e) helm_charts → helm_fts.
	if rx.helmRepo != nil {
		helms, err := rx.helmRepo.ListByRepo(ctx, repoID)
		if err != nil {
			return fmt.Errorf("fts: reindex list helm (repo=%d): %w", repoID, err)
		}
		for _, c := range helms {
			if err := IndexHelm(ctx, tx, repoID, c.Name, c.Version, c.AppVersion, c.Description); err != nil {
				return err
			}
		}
	}
	// (f) raw_files → artifacts_fts (mirrors raw/put.go:92 —
	//     IndexArtifact(repoID, path, "", path)).
	rawRows, err := tx.QueryContext(ctx, `SELECT path FROM raw_files WHERE repo_id = ?`, repoID)
	if err != nil {
		return fmt.Errorf("fts: reindex list raw_files (repo=%d): %w", repoID, err)
	}
	var rawPaths []string
	for rawRows.Next() {
		var pth string
		if err := rawRows.Scan(&pth); err != nil {
			_ = rawRows.Close()
			return fmt.Errorf("fts: reindex scan raw path (repo=%d): %w", repoID, err)
		}
		rawPaths = append(rawPaths, pth)
	}
	if err := rawRows.Close(); err != nil {
		return fmt.Errorf("fts: reindex close raw rows (repo=%d): %w", repoID, err)
	}
	for _, pth := range rawPaths {
		if err := IndexArtifact(ctx, tx, repoID, pth, "", pth); err != nil {
			return err
		}
	}
	// (g) docker_manifests → artifacts_fts. Mirror oci/manifests.go:660 minus
	//     the expensive manifest-body parse: we use the latest tag for that
	//     digest (joined from docker_tags) as the version field, and ftsName
	//     = "<projectName>/<repoType>/<repoName>".
	manRows, err := tx.QueryContext(ctx, `
		SELECT m.digest,
		       (SELECT t.tag FROM docker_tags t
		         WHERE t.repo_id = m.repo_id AND t.digest = m.digest LIMIT 1) AS tag
		  FROM docker_manifests m WHERE m.repo_id = ?
	`, repoID)
	if err != nil {
		return fmt.Errorf("fts: reindex list manifests (repo=%d): %w", repoID, err)
	}
	type man struct {
		digest string
		tag    sql.NullString
	}
	var mans []man
	for manRows.Next() {
		var m man
		if err := manRows.Scan(&m.digest, &m.tag); err != nil {
			_ = manRows.Close()
			return fmt.Errorf("fts: reindex scan manifest (repo=%d): %w", repoID, err)
		}
		mans = append(mans, m)
	}
	if err := manRows.Close(); err != nil {
		return fmt.Errorf("fts: reindex close manifest rows (repo=%d): %w", repoID, err)
	}
	ftsName := fmt.Sprintf("%s/%s/%s", projectName, repoType, repoName)
	for _, m := range mans {
		ver := ""
		if m.tag.Valid {
			ver = m.tag.String
		}
		if err := IndexArtifact(ctx, tx, repoID, ftsName, ver, m.digest); err != nil {
			return err
		}
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
