package metadata

import (
	"context"
	"database/sql"
	"fmt"
)

// pragmaDSNValues is the D-09 pragma list carried into every connection via
// modernc.org/sqlite's `_pragma=<stmt>` DSN extension. Each entry becomes one
// `_pragma=<entry>` query-string parameter on the DSN (see ensureDSN in
// db.go), so every connection both pools open gets these pragmas applied
// before the first statement runs.
//
// Format note: modernc.org/sqlite expects function-call syntax inside
// _pragma (e.g. `foreign_keys(on)`), not `PRAGMA foreign_keys=ON` statement
// form. The driver runs each as `PRAGMA <value>;` under the hood.
//
// Order matters: journal_mode must be set before any write occurs, but in the
// DSN path the driver applies the list once at connection open, before any
// user statement runs, so the ordering is implicitly correct.
var pragmaDSNValues = []string{
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
	"busy_timeout(5000)",
	"cache_size(-65536)",
	"temp_store(MEMORY)",
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
