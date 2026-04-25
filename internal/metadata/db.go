// Package metadata owns the SQLite metadata substrate for OmniRepo.
//
// It implements the D-09/D-10 reader/writer split: two *sql.DB handles against
// the same SQLite file, reader pool sized 8, writer pool sized 1. Every write
// transaction is BEGIN IMMEDIATE (via the modernc.org/sqlite `_txlock=immediate`
// DSN extension, documented in internal/metadata/tx.go) so concurrent writers
// serialize cleanly without ever returning SQLITE_BUSY — pinning pitfall P2.
//
// modernc.org/sqlite is imported here (moving it out of the tools.go pin) and
// registered as driver name "sqlite".
package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // registers driver "sqlite"
)

// DBTimestampLayout is the canonical on-disk representation for any
// timestamp column compared lexicographically — e.g. sessions.expires_at,
// api_keys.last_used_at. Fixed-width 30-char ISO-8601 with nanosecond
// precision avoids the Go-%v format variability that broke F-04.2
// (audit) and surfaced as F-04.3 (sessions, api-keys).
//
// Mirrors audit.DBTimestampLayout verbatim; duplicated here because the
// audit package imports metadata, so we cannot import it back without a
// cycle. Keep the two values identical.
const DBTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// DB wraps the reader/writer *sql.DB pair. Callers use DB.Reader for read
// queries and DB.WriteTx(ctx, fn) for any writes.
//
// writeTxFailpoint is a one-shot test-only error injection slot consumed by
// WriteTx (see tx.go). The slot is nil in production; SetWriteTxFailpointForTest
// arms it. The "ForTest" suffix on the public setter is a grep-gate signal
// that production code must not call it. The mutex guards reads (consume)
// and writes (set) so a t.Cleanup goroutine can safely clear the slot
// alongside an in-flight WriteTx (the writer pool itself is size-1 so
// WriteTx calls already serialize at the database/sql layer).
type DB struct {
	Reader *sql.DB // SetMaxOpenConns(8)
	Writer *sql.DB // SetMaxOpenConns(1), SetMaxIdleConns(1)

	path string

	writeTxFailpointMu sync.Mutex // guards writeTxFailpoint
	writeTxFailpoint   error      // one-shot test injection (nil in production)
}

// readerPoolSize is D-10's reader-pool default (N=8).
const readerPoolSize = 8

// Open returns a DB handle backed by the SQLite file (or memory DSN) at path.
//
// Pragmas are appended to the DSN via modernc.org/sqlite's `_pragma=` DSN
// extension so EVERY connection the reader and writer pools hand out gets the
// D-09 pragma list at open time — not just the first one. modernc.org/sqlite
// does not share pragma state across connections, and the previous strategy
// (apply pragmas to one conn per pool) left the other 7 reader connections
// with driver defaults (foreign_keys=OFF, busy_timeout=0). WR-01 fixed.
//
// Also transparently appends ?_txlock=immediate so every sql.Tx is promoted
// to BEGIN IMMEDIATE (see tx.go) — pitfall P2 mitigation.
//
// Compile-option probes (FTS5 + JSON1) run once against a writer connection.
func Open(path string) (*DB, error) {
	dsn := ensureDSN(path)

	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("metadata: open reader: %w", err)
	}
	reader.SetMaxOpenConns(readerPoolSize)
	reader.SetMaxIdleConns(readerPoolSize)

	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("metadata: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	ctx := context.Background()

	// One-shot compile-option probe (FTS5 + JSON1) on a writer conn. The
	// pragmas themselves are now carried in the DSN via _pragma=, so they
	// apply to every connection both pools open.
	if err := applyOnConn(ctx, writer, checkCompileOptions); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}

	return &DB{Reader: reader, Writer: writer, path: path}, nil
}

// Close closes both pools. Safe to call on a nil *DB (no-op) so test cleanup
// stays simple.
func (db *DB) Close() error {
	if db == nil {
		return nil
	}
	var first error
	if db.Reader != nil {
		if err := db.Reader.Close(); err != nil && first == nil {
			first = err
		}
	}
	if db.Writer != nil {
		if err := db.Writer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Path returns the DSN the DB was opened with (before ensureDSN).
func (db *DB) Path() string { return db.path }

// applyOnConn grabs one connection from pool, runs fn, releases.
func applyOnConn(ctx context.Context, pool *sql.DB, fn func(context.Context, *sql.Conn) error) error {
	conn, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("metadata: conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return fn(ctx, conn)
}

// ensureDSN guarantees the DSN carries:
//
//  1. `_txlock=immediate` — modernc.org/sqlite's documented extension for
//     forcing every sql.BeginTx / db.Begin to open as BEGIN IMMEDIATE instead
//     of DEFERRED. Pitfall P2 cornerstone.
//  2. The D-09 PRAGMA list via `_pragma=...` — every new connection on both
//     pools inherits foreign_keys=ON, busy_timeout=5000, journal_mode=WAL,
//     synchronous=NORMAL, cache_size=-65536, temp_store=MEMORY.
//
// Accepts both bare paths ("/tmp/a.db") and DSNs with existing query strings
// ("file:/tmp/a.db?cache=shared"). Idempotent for each parameter: already-
// present `_txlock=immediate` or `_pragma=` values are preserved.
func ensureDSN(path string) string {
	const txlock = "_txlock=immediate"
	params := make([]string, 0, len(pragmaDSNValues)+1)
	if !strings.Contains(path, txlock) {
		params = append(params, txlock)
	}
	for _, p := range pragmaDSNValues {
		v := "_pragma=" + p
		// Idempotency: avoid duplicating if caller already passed it.
		if !strings.Contains(path, v) {
			params = append(params, v)
		}
	}
	if len(params) == 0 {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + strings.Join(params, "&")
}
