package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// resetTables is the authoritative v1.5 Phase 1 wipe list. Every migration
// that adds a physical or FTS5 virtual table MUST append its name here. The
// invariant test TestResetCoversEveryTable (reset_test.go) queries
// sqlite_master and fails if any table is missing from this slice AND not
// in the explicit excludes set (schema_migrations, users, settings — users +
// settings are handled by the preservation clauses in Reset below).
//
// Ordering doesn't matter — Reset applies PRAGMA foreign_keys=OFF at the
// connection level before BEGIN, so FK cycles cannot trip us. DELETE
// statements run in whatever order this slice specifies; PRAGMA
// foreign_key_check runs as a pre-commit audit.
//
// FTS5 virtual tables (post-migration-005 they own their content; DELETE
// FROM works) ARE included. FTS5 auxiliary shadow tables (*_data, *_idx,
// *_content, *_docsize, *_config) are auto-maintained by SQLite when you
// DELETE FROM the parent virtual table — do NOT list them here.
var resetTables = []string{
	// 001_initial
	"sessions", "projects", "project_members", "api_keys", "repos",
	"s3_buckets", "audit_log", "blob_uploads",
	"repos_fts", "artifacts_fts", "cves_fts",
	// 002_jobs
	"sync_jobs", "scans", "vulnerabilities",
	// 003_oci
	"docker_blobs", "docker_manifests", "docker_tags",
	"blob_upload_sessions",
	// 004_upstream_creds
	"upstream_creds",
	// 006_raw_files
	"raw_files",
	// 008_signing_keys
	"signing_keys",
	// 009_apt_suites
	"apt_suites",
	// 010_rpm_packages
	"rpm_packages",
	// 011_deb_packages
	"deb_packages",
	// 012_pypi_files
	"pypi_files",
	// 013_helm_charts
	"helm_charts",
	// 014_protocol_fts
	"rpm_fts", "deb_fts", "pypi_fts", "helm_fts",
	// 016_s3_access_keys
	"s3_access_keys",
	// 017_git_extensions
	"git_refs",
	// 018_s3_objects
	"s3_objects",
	// 019_s3_multipart
	"s3_multipart_uploads", "s3_multipart_parts",
	// 020_maintenance_trivydb
	"trivy_db_meta",
	// NOTE: users + settings are handled separately (preservation clauses
	// in DB.Reset). schema_migrations is NEVER wiped (reset is a data op,
	// not a schema op).
}

// preservedSettingsKeys are the rows that MUST survive a Reset.
//
//  1. Bootstrap secrets — app.Run materialises in-memory handles for
//     these at boot (BootEnsureDockerJWTSecret / BootEnsureAEADKey); wiping
//     the rows strands any code path that reloads them.
//  2. Boot integrity-check metadata — RunBootIntegrityCheck writes these
//     exactly once at startup (Phase 10 DBHEALTH-06, D-16). The DBHealth
//     UI card reads them to render the "last checked" status; wiping
//     leaves the card blank until the next server restart or manual
//     integrity run. The dev-only /admin/_reset contract is "wipe per-test
//     state" — boot metadata isn't per-test state, so preserve it.
var preservedSettingsKeys = []any{
	"docker_token_hmac_secret",
	"upstream_creds_aead_key",
	"db.integrity_check.status",
	"db.integrity_check.checked_at",
	"db.integrity_check.duration_ms",
	"db.integrity_check.last_manual_at",
}

// Reset wipes every non-super-admin table in a single writer transaction,
// preserving super-admin users rows and bootstrap settings keys.
//
// DEV-ONLY: callers must gate on OMNIREPO_DEV=1 before invoking. This
// helper performs NO env check — that is the responsibility of the HTTP
// mount point in internal/api/admin_reset.go.
//
// FK strategy mirrors internal/metadata/migrations/runner.go:runOne —
// connection-scoped PRAGMA foreign_keys=OFF BEFORE BeginTx (SQLite spec:
// the pragma is a no-op inside a pending transaction) with a deferred
// restore that fires even on panic. Pre-commit PRAGMA foreign_key_check
// audits the wipe and rolls back on any dangling-FK violation.
func (db *DB) Reset(ctx context.Context) (err error) {
	conn, connErr := db.Writer.Conn(ctx)
	if connErr != nil {
		return fmt.Errorf("metadata.Reset: acquire conn: %w", connErr)
	}
	defer func() { _ = conn.Close() }()

	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("metadata.Reset: disable foreign_keys: %w", err)
	}
	defer func() {
		if _, rErr := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); rErr != nil && err == nil {
			err = fmt.Errorf("metadata.Reset: restore foreign_keys: %w", rErr)
		}
	}()

	tx, beginErr := conn.BeginTx(ctx, nil) // BEGIN IMMEDIATE via _txlock=immediate DSN
	if beginErr != nil {
		return fmt.Errorf("metadata.Reset: begin: %w", beginErr)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone && err == nil {
			err = fmt.Errorf("metadata.Reset: rollback: %w", rbErr)
		}
	}()

	for _, table := range resetTables {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("metadata.Reset: wipe %s: %w", table, err)
		}
	}

	// Preservation clauses: keep super-admin users + bootstrap secrets.
	if _, err = tx.ExecContext(ctx, "DELETE FROM users WHERE is_super_admin = 0"); err != nil {
		return fmt.Errorf("metadata.Reset: wipe non-admin users: %w", err)
	}
	placeholders := strings.Repeat("?,", len(preservedSettingsKeys))
	placeholders = strings.TrimRight(placeholders, ",")
	if _, err = tx.ExecContext(ctx,
		"DELETE FROM settings WHERE key NOT IN ("+placeholders+")",
		preservedSettingsKeys...,
	); err != nil {
		return fmt.Errorf("metadata.Reset: wipe non-bootstrap settings: %w", err)
	}

	// Pre-commit audit. If FKs=OFF let us orphan anything, this rolls back.
	if err = resetAssertNoFKViolations(ctx, tx); err != nil {
		return fmt.Errorf("metadata.Reset: foreign_key_check: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("metadata.Reset: commit: %w", err)
	}
	committed = true
	return nil
}

// resetAssertNoFKViolations is a local copy of the 5-line foreign_key_check
// pattern used by internal/metadata/migrations/runner.go:assertNoFKViolations.
// Kept local (not imported from migrations) to avoid a metadata→migrations
// package dep. If migrations later exports its helper, collapse to a call.
func resetAssertNoFKViolations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var violations []string
	for rows.Next() {
		var table, parent sql.NullString
		var rowID, fkID sql.NullInt64
		if scanErr := rows.Scan(&table, &rowID, &parent, &fkID); scanErr != nil {
			return fmt.Errorf("foreign_key_check scan: %w", scanErr)
		}
		violations = append(violations,
			fmt.Sprintf("%s(rowid=%d) → %s(fkid=%d)",
				table.String, rowID.Int64, parent.String, fkID.Int64))
	}
	if rErr := rows.Err(); rErr != nil {
		return fmt.Errorf("foreign_key_check iterate: %w", rErr)
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign_key_check failed: %d violation(s): %s",
			len(violations), strings.Join(violations, "; "))
	}
	return nil
}
