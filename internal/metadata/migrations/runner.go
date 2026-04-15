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
	defer rows.Close()
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
func runOne(ctx context.Context, writer *sql.DB, stem, body string) (err error) {
	tx, beginErr := writer.BeginTx(ctx, nil)
	if beginErr != nil {
		return fmt.Errorf("begin: %w", beginErr)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
			}
		}
	}()
	// Execute the whole file as one batch — modernc.org/sqlite supports
	// multi-statement ExecContext.
	if _, err = tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(name) VALUES (?)", stem); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
