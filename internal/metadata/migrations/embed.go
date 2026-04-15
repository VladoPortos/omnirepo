// Package migrations owns the hand-rolled SQLite migration runner (D-11).
//
// Migration files live alongside this package as NNN_<name>.up.sql and
// NNN_<name>.down.sql and are bundled into the binary via //go:embed. The
// runner applies each .up.sql whose stem is not yet recorded in
// schema_migrations, in lexicographic order. Each file runs inside its own
// metadata.WriteTx so a broken statement rolls back cleanly and
// schema_migrations stays authoritative.
package migrations

import "embed"

// FS carries every .sql file in this directory. Exported so tests can
// introspect the available migrations directly; production code calls Apply
// and Status which read from this FS by default.
//
//go:embed *.sql
var FS embed.FS
