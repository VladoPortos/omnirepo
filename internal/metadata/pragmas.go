package metadata

import (
	"context"
	"database/sql"
	"fmt"
)

// pragmaStmts is the D-09 pragma list applied to every SQLite connection the
// reader and writer pools hand out. Order matters: journal_mode must be set
// before any write occurs.
var pragmaStmts = []string{
	"PRAGMA journal_mode=WAL",
	"PRAGMA synchronous=NORMAL",
	"PRAGMA foreign_keys=ON",
	"PRAGMA busy_timeout=5000",
	"PRAGMA cache_size=-65536",
	"PRAGMA temp_store=MEMORY",
}

// applyPragmas executes the D-09 pragma list on the given connection. The
// journal_mode statement returns a row; the rest are no-result statements but
// modernc.org/sqlite happily accepts them via ExecContext. We run everything
// as queries and discard the rows so PRAGMA journal_mode=WAL's result doesn't
// break.
func applyPragmas(ctx context.Context, conn *sql.Conn) error {
	for _, stmt := range pragmaStmts {
		rows, err := conn.QueryContext(ctx, stmt)
		if err != nil {
			return fmt.Errorf("metadata: apply %s: %w", stmt, err)
		}
		// Drain + close to release the connection.
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("metadata: %s: %w", stmt, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("metadata: %s close: %w", stmt, err)
		}
	}
	return nil
}

// checkCompileOptions asserts the modernc.org/sqlite build has the features
// OmniRepo relies on: ENABLE_FTS5 (global search) and the JSON1 extension
// (audit details, settings blobs).
//
// Note on ENABLE_JSON1: as of SQLite 3.38 the JSON1 extension is compiled in
// unconditionally and is no longer reported by PRAGMA compile_options — so a
// compile_options lookup returns 0 on every modernc.org/sqlite build even
// though json() works. We therefore probe FTS5 via sqlite_compileoption_used
// (still reported) and probe JSON1 functionally by executing `SELECT json()`.
// Either missing surfaces a wrapped error naming the feature — per D-09.
func checkCompileOptions(ctx context.Context, conn *sql.Conn) error {
	var hasFTS5 int
	if err := conn.QueryRowContext(ctx, "SELECT sqlite_compileoption_used(?)", "ENABLE_FTS5").Scan(&hasFTS5); err != nil {
		return fmt.Errorf("metadata: compile_options check for ENABLE_FTS5: %w", err)
	}
	if hasFTS5 != 1 {
		return fmt.Errorf("metadata: sqlite build missing required compile option ENABLE_FTS5")
	}

	// Functional probe for ENABLE_JSON1 — must return a valid JSON text.
	var out string
	if err := conn.QueryRowContext(ctx, `SELECT json('{"probe":1}')`).Scan(&out); err != nil {
		return fmt.Errorf("metadata: sqlite build missing required compile option ENABLE_JSON1: %w", err)
	}
	return nil
}
