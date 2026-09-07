package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// preservedSettingsKeys are the rows that MUST survive a Reset.
//
//  1. Bootstrap secrets — app.Run materialises in-memory handles for
//     these at boot (BootEnsureDockerJWTSecret / BootEnsureAEADKey); wiping
//     the rows strands any code path that reloads them.
//  2. Boot integrity-check metadata — RunBootIntegrityCheck writes these
//     exactly once at startup. The DBHealth
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

	// Derive the inventory from the current schema. table_list distinguishes
	// FTS shadow tables from their virtual parents; only the parents are wiped.
	rows, queryErr := tx.QueryContext(ctx, `SELECT name FROM pragma_table_list
        WHERE schema='main' AND type IN ('table','virtual')
        AND name NOT GLOB 'sqlite_*'
        AND name NOT IN ('schema_migrations','users','settings')`)
	if queryErr != nil {
		return fmt.Errorf("metadata.Reset: list tables: %w", queryErr)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, table := range tables {
		if _, err = tx.ExecContext(ctx, `DELETE FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`); err != nil {
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
