package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Apply runs every pending .up.sql migration from the embedded FS against
// writer. It returns the stems (e.g. "001_initial") of migrations it applied
// in this call. Repeat calls with no pending files return an empty slice.
//
// Each migration runs inside a single BEGIN IMMEDIATE write tx via the
// metadata.WriteTx pattern — we can't import metadata here without a cycle,
// so callers pass in a *sql.DB that already has _txlock=immediate set (as
// metadata.Open does).
func Apply(ctx context.Context, writer *sql.DB) ([]string, error) {
	return applyFS(ctx, writer, FS)
}

// Status returns (applied, pending) stems where `applied` is every row in
// schema_migrations and `pending` is every .up.sql in FS whose stem is not yet
// in schema_migrations. Both lists are sorted lexicographically.
func Status(ctx context.Context, reader *sql.DB) ([]string, []string, error) {
	return statusFS(ctx, reader, FS)
}

// applyFS is the testable core: callers can pass a fstest.MapFS to exercise
// broken-migration and ordering behaviour without touching the real embed.FS.
func applyFS(ctx context.Context, writer *sql.DB, source fs.FS) ([]string, error) {
	if err := ensureMigrationsTable(ctx, writer); err != nil {
		return nil, err
	}
	upFiles, err := listUpFiles(source)
	if err != nil {
		return nil, err
	}
	already, err := loadApplied(ctx, writer)
	if err != nil {
		return nil, err
	}

	var applied []string
	for _, f := range upFiles {
		stem := upStem(f)
		if already[stem] {
			continue
		}
		body, rerr := fs.ReadFile(source, f)
		if rerr != nil {
			return applied, fmt.Errorf("migrations: read %s: %w", f, rerr)
		}
		if err := runOne(ctx, writer, stem, string(body)); err != nil {
			return applied, fmt.Errorf("migration %s: %w", stem, err)
		}
		applied = append(applied, stem)
	}
	return applied, nil
}

// statusFS mirrors applyFS: enumerates source, reads schema_migrations, diffs.
func statusFS(ctx context.Context, reader *sql.DB, source fs.FS) ([]string, []string, error) {
	// schema_migrations may not exist yet on an empty DB; treat "no such table"
	// as "nothing applied" rather than an error, so Status works before the
	// first Apply.
	applied, err := loadAppliedSoft(ctx, reader)
	if err != nil {
		return nil, nil, err
	}
	upFiles, err := listUpFiles(source)
	if err != nil {
		return nil, nil, err
	}
	var appliedSlice, pending []string
	for _, f := range upFiles {
		stem := upStem(f)
		if applied[stem] {
			appliedSlice = append(appliedSlice, stem)
		} else {
			pending = append(pending, stem)
		}
	}
	sort.Strings(appliedSlice)
	sort.Strings(pending)
	return appliedSlice, pending, nil
}

// ensureMigrationsTable creates schema_migrations if absent. Idempotent. This
// is the one statement that runs outside the per-migration tx because every
// migration depends on the table existing.
func ensureMigrationsTable(ctx context.Context, writer *sql.DB) error {
	const ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
		name        TEXT PRIMARY KEY,
		applied_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := writer.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrations: ensure schema_migrations table: %w", err)
	}
	return nil
}

func loadApplied(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	return queryApplied(ctx, db)
}

// loadAppliedSoft tolerates a missing schema_migrations table (returns empty
// map). Used by Status so a pre-Apply call does not error.
func loadAppliedSoft(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	m, err := queryApplied(ctx, db)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return map[string]bool{}, nil
	}
	return m, err
}

func queryApplied(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

func listUpFiles(source fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("migrations: readdir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".up.sql") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func upStem(filename string) string {
	return strings.TrimSuffix(filename, ".up.sql")
}

// runOne executes a single migration's SQL body inside a BEGIN IMMEDIATE tx
// (on writers opened with _txlock=immediate) and records it in
// schema_migrations as the last statement before commit. Any error rolls the
// whole thing back.
//
// We flip PRAGMA foreign_keys=OFF at the connection level *before* BEGIN and
// restore it after COMMIT. This is the canonical SQLite recipe for
// migrations that do table rebuilds (DROP + CREATE + RENAME): the
// intermediate state between DROP old_table and RENAME new_table→old_table
// briefly leaves FKs dangling, and neither `defer_foreign_keys=ON` nor the
// RENAME's FK-rewrite behaviour catches every edge case (modernc + real
// SQLite both raise a generic "FOREIGN KEY constraint failed" at COMMIT
// otherwise).
//
// Re-enabling FK enforcement at the connection level does NOT retroactively
// validate rows inserted while FKs were disabled — it only governs future
// writes. To catch the "migration INSERTed an orphan while FK was OFF"
// class of mistake, we run PRAGMA foreign_key_check inside the tx right
// before Commit (so a failure rolls back the body AND prevents recording
// the migration as applied).
func runOne(ctx context.Context, writer *sql.DB, stem, body string) (err error) {
	// Grab a dedicated connection for the whole migration so the PRAGMAs
	// land on the same connection as the tx. Without this, the pool could
	// hand us a different conn for ExecContext vs BeginTx.
	conn, connErr := writer.Conn(ctx)
	if connErr != nil {
		return fmt.Errorf("acquire conn: %w", connErr)
	}
	defer func() { _ = conn.Close() }()

	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	// Best-effort restore. Runs even on panic so the writer pool doesn't
	// leak a FK-disabled conn back into circulation. If restore fails and
	// we otherwise had no error, surface the restore error.
	defer func() {
		if _, rErr := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); rErr != nil && err == nil {
			err = fmt.Errorf("restore foreign_keys: %w", rErr)
		}
	}()

	tx, beginErr := conn.BeginTx(ctx, nil)
	if beginErr != nil {
		return fmt.Errorf("begin: %w", beginErr)
	}
	// Rollback on any return path where Commit wasn't reached — including
	// panic. Using a local flag instead of reading the named `err` means a
	// panic (which leaves err nil) still triggers rollback, closing the tx
	// cleanly before the panic propagates.
	committed := false
	defer func() {
		if committed {
			return
		}
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone && err == nil {
			err = fmt.Errorf("rollback: %w", rbErr)
		}
	}()
	// Execute the whole file as one batch — modernc.org/sqlite supports
	// multi-statement ExecContext.
	if _, err = tx.ExecContext(ctx, body); err != nil {
		return err
	}
	// Pre-commit data-integrity audit. foreign_keys=OFF during the body
	// means any rows the migration INSERTed with a bad FK would commit
	// silently. PRAGMA foreign_key_check yields one row per violating
	// row; a non-empty result set means the migration broke integrity
	// and the whole tx must roll back — running this INSIDE the tx
	// guarantees we never record the migration as applied on failure.
	if err = assertNoFKViolations(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(name) VALUES (?)", stem); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

// assertNoFKViolations runs PRAGMA foreign_key_check against the current
// transaction and returns a descriptive error if any rows come back. Called
// from runOne before Commit so a migration that leaves orphans never lands
// a schema_migrations row.
func assertNoFKViolations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var violations []string
	for rows.Next() {
		var table, parent string
		var rowid, fkid sql.NullInt64
		if scanErr := rows.Scan(&table, &rowid, &parent, &fkid); scanErr != nil {
			return fmt.Errorf("foreign_key_check scan: %w", scanErr)
		}
		violations = append(violations, fmt.Sprintf("%s(rowid=%v) → %s(fkid=%v)", table, rowid.Int64, parent, fkid.Int64))
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
